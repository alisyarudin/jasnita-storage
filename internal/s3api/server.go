// Package s3api mengimplementasikan subset API S3 yang dibutuhkan klien umum
// (Laravel/Flysystem, aws cli, rclone): bucket, objek, listing, multipart, presigned URL.
package s3api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/alisyarudin/jasnita-storage/internal/blob"
	"github.com/alisyarudin/jasnita-storage/internal/meta"
	"github.com/alisyarudin/jasnita-storage/internal/sigv4"
)

const xmlNS = "http://s3.amazonaws.com/doc/2006-03-01/"
const iso8601 = "2006-01-02T15:04:05.000Z"

type Server struct {
	Meta   *meta.DB
	Blob   *blob.Store
	Region string
	Domain string // opsional: virtual-host style
	Log    *slog.Logger
}

// reqInfo adalah konteks per request.
type reqInfo struct {
	id     string
	key    *meta.AccessKey // nil = anonim
	sig    *sigv4.Result
	bucket string
	object string
}

// ---------- error S3 ----------

type s3Error struct {
	Code    string
	Message string
	Status  int
}

func (e *s3Error) Error() string { return e.Code + ": " + e.Message }

var statusByCode = map[string]int{
	"NoSuchBucket": 404, "NoSuchKey": 404, "NoSuchUpload": 404,
	"BucketAlreadyExists": 409, "BucketAlreadyOwnedByYou": 409, "BucketNotEmpty": 409,
	"AccessDenied": 403, "InvalidAccessKeyId": 403, "SignatureDoesNotMatch": 403, "RequestTimeTooSkewed": 403,
	"AuthorizationHeaderMalformed": 400, "AuthorizationQueryParametersError": 400,
	"InvalidArgument": 400, "MalformedXML": 400, "InvalidPart": 400, "InvalidPartOrder": 400,
	"BadDigest": 400, "XAmzContentSHA256Mismatch": 400, "InvalidBucketName": 400, "InvalidRequest": 400,
	"KeyTooLongError": 400, "IncompleteBody": 400, "EntityTooSmall": 400,
	"MethodNotAllowed": 405, "NotImplemented": 501, "InternalError": 500, "PreconditionFailed": 412,
}

func newErr(code, msg string) *s3Error {
	st, ok := statusByCode[code]
	if !ok {
		st = 400
	}
	return &s3Error{Code: code, Message: msg, Status: st}
}

var (
	errNoSuchBucket = newErr("NoSuchBucket", "Bucket tidak ditemukan")
	errNoSuchKey    = newErr("NoSuchKey", "Objek tidak ditemukan")
	errAccessDenied = newErr("AccessDenied", "Akses ditolak")
	errNoSuchUpload = newErr("NoSuchUpload", "Upload multipart tidak ditemukan")
	errInternal     = newErr("InternalError", "Terjadi kesalahan internal")
)

type errorXML struct {
	XMLName   xml.Name `xml:"Error"`
	Code      string   `xml:"Code"`
	Message   string   `xml:"Message"`
	Resource  string   `xml:"Resource"`
	RequestID string   `xml:"RequestId"`
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, info *reqInfo, err error) {
	var se *s3Error
	var ae *sigv4.Error
	switch {
	case errors.As(err, &se):
	case errors.As(err, &ae):
		se = newErr(ae.Code, ae.Message)
	case errors.Is(err, meta.ErrNotFound):
		se = errNoSuchKey
	default:
		s.Log.Error("internal", "err", err, "path", r.URL.Path, "req", info.id)
		se = errInternal
	}
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(se.Status)
	if r.Method == http.MethodHead {
		return
	}
	writeXML(w, errorXML{Code: se.Code, Message: se.Message, Resource: r.URL.Path, RequestID: info.id})
}

func writeXML(w http.ResponseWriter, v any) {
	w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Encode(v)
}

func (s *Server) writeXMLOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	writeXML(w, v)
}

// ---------- routing ----------

func requestID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return strings.ToUpper(hex.EncodeToString(b))
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(code int) {
	sw.status = code
	sw.ResponseWriter.WriteHeader(code)
}

func (sw *statusWriter) Write(b []byte) (int, error) {
	if sw.status == 0 {
		sw.status = 200
	}
	return sw.ResponseWriter.Write(b)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	sw := &statusWriter{ResponseWriter: w}
	info := &reqInfo{id: requestID()}
	h := sw.Header()
	h.Set("Server", "JasStore")
	h.Set("X-Amz-Request-Id", info.id)
	h.Set("Accept-Ranges", "bytes")
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Expose-Headers", "ETag, Content-Length, Content-Range, Last-Modified, x-amz-request-id, x-amz-meta-*")

	if r.Method == http.MethodOptions {
		h.Set("Access-Control-Allow-Methods", "GET, PUT, POST, DELETE, HEAD, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "*")
		h.Set("Access-Control-Max-Age", "86400")
		sw.WriteHeader(http.StatusNoContent)
		return
	}

	info.bucket, info.object = s.splitPath(r)
	err := s.handle(sw, r, info)
	if err != nil {
		s.writeError(sw, r, info, err)
	}
	ak := "-"
	if info.key != nil {
		ak = info.key.AccessKey
	}
	s.Log.Info("s3", "m", r.Method, "p", r.URL.Path, "q", r.URL.RawQuery, "st", sw.status, "ak", ak, "ms", time.Since(start).Milliseconds(), "req", info.id)
}

func (s *Server) splitPath(r *http.Request) (bucket, key string) {
	p := strings.TrimPrefix(r.URL.Path, "/")
	if s.Domain != "" {
		host := r.Host
		if hh, _, err := net.SplitHostPort(host); err == nil {
			host = hh
		}
		if strings.HasSuffix(host, "."+s.Domain) {
			return strings.TrimSuffix(host, "."+s.Domain), p
		}
	}
	bucket, key, _ = strings.Cut(p, "/")
	return bucket, key
}

func (s *Server) authenticate(r *http.Request, info *reqInfo) error {
	if !sigv4.IsSigned(r) {
		return nil // anonim
	}
	ctx := r.Context()
	lookup := func(ak string) (string, bool) {
		k, err := s.Meta.GetAccessKey(ctx, ak)
		if err != nil || !k.Enabled {
			return "", false
		}
		info.key = k
		return k.SecretKey, true
	}
	res, err := sigv4.Verify(r, lookup, time.Now())
	if err != nil {
		info.key = nil
		return err
	}
	info.sig = res
	return nil
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request, info *reqInfo) error {
	if err := s.authenticate(r, info); err != nil {
		return err
	}
	ctx := r.Context()
	q := r.URL.Query()

	if info.bucket == "" {
		if r.Method == http.MethodGet {
			return s.listBuckets(ctx, w, info)
		}
		return newErr("MethodNotAllowed", "Metode tidak didukung pada root")
	}

	if info.object == "" {
		switch r.Method {
		case http.MethodPut:
			return s.createBucket(ctx, w, r, info)
		case http.MethodDelete:
			return s.deleteBucket(ctx, w, info)
		case http.MethodHead:
			return s.headBucket(ctx, w, info)
		case http.MethodPost:
			if q.Has("delete") {
				return s.deleteObjects(ctx, w, r, info)
			}
			return newErr("NotImplemented", "Operasi POST bucket ini belum didukung")
		case http.MethodGet:
			switch {
			case q.Has("location"):
				return s.getBucketLocation(ctx, w, info)
			case q.Has("uploads"):
				return s.listMultipartUploads(ctx, w, r, info)
			case q.Has("versions"):
				return s.listObjectVersions(ctx, w, r, info)
			case q.Has("versioning"):
				return s.getBucketVersioning(ctx, w, info)
			case q.Has("acl"):
				return s.getBucketACL(ctx, w, info)
			case q.Has("policy"), q.Has("cors"), q.Has("lifecycle"), q.Has("encryption"), q.Has("tagging"),
				q.Has("website"), q.Has("logging"), q.Has("notification"), q.Has("replication"), q.Has("object-lock"),
				q.Has("accelerate"), q.Has("requestPayment"), q.Has("ownershipControls"), q.Has("publicAccessBlock"):
				return newErr("NotImplemented", "Sub-resource bucket ini belum didukung")
			}
			return s.listObjects(ctx, w, r, info)
		}
		return newErr("MethodNotAllowed", "Metode tidak didukung pada bucket")
	}

	switch r.Method {
	case http.MethodGet:
		if q.Has("uploadId") {
			return s.listParts(ctx, w, r, info)
		}
		if q.Has("acl") {
			return s.getObjectACL(ctx, w, info)
		}
		if q.Has("tagging") || q.Has("legal-hold") || q.Has("retention") || q.Has("attributes") {
			return newErr("NotImplemented", "Sub-resource objek ini belum didukung")
		}
		return s.getObject(ctx, w, r, info)
	case http.MethodHead:
		return s.getObject(ctx, w, r, info)
	case http.MethodPut:
		if q.Has("uploadId") && q.Has("partNumber") {
			return s.uploadPart(ctx, w, r, info)
		}
		if r.Header.Get("X-Amz-Copy-Source") != "" {
			return s.copyObject(ctx, w, r, info)
		}
		if q.Has("acl") || q.Has("tagging") {
			// diterima tanpa efek agar klien yang mengirim ACL tidak gagal
			w.WriteHeader(http.StatusOK)
			return nil
		}
		return s.putObject(ctx, w, r, info)
	case http.MethodPost:
		if q.Has("uploads") {
			return s.initiateMultipart(ctx, w, r, info)
		}
		if q.Has("uploadId") {
			return s.completeMultipart(ctx, w, r, info)
		}
		return newErr("NotImplemented", "Operasi POST objek ini belum didukung")
	case http.MethodDelete:
		if q.Has("uploadId") {
			return s.abortMultipart(ctx, w, r, info)
		}
		return s.deleteObject(ctx, w, info)
	}
	return newErr("MethodNotAllowed", "Metode tidak didukung pada objek")
}

// ---------- otorisasi ----------

// loadBucket mengambil bucket, atau NoSuchBucket.
func (s *Server) loadBucket(ctx context.Context, name string) (*meta.Bucket, error) {
	b, err := s.Meta.GetBucket(ctx, name)
	if errors.Is(err, meta.ErrNotFound) {
		return nil, errNoSuchBucket
	}
	return b, err
}

func (s *Server) requireRead(ctx context.Context, info *reqInfo, allowPublic bool) (*meta.Bucket, error) {
	b, err := s.loadBucket(ctx, info.bucket)
	if err != nil {
		return nil, err
	}
	if info.key != nil && info.key.CanRead(b.Name) {
		return b, nil
	}
	if allowPublic && b.Public {
		return b, nil
	}
	return nil, errAccessDenied
}

func (s *Server) requireWrite(ctx context.Context, info *reqInfo) (*meta.Bucket, error) {
	b, err := s.loadBucket(ctx, info.bucket)
	if err != nil {
		return nil, err
	}
	if info.key != nil && info.key.CanWrite(b.Name) {
		return b, nil
	}
	return nil, errAccessDenied
}

var bucketNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
var ipRe = regexp.MustCompile(`^\d+\.\d+\.\d+\.\d+$`)

// ValidBucketName mengikuti aturan penamaan bucket S3.
func ValidBucketName(name string) bool {
	if !bucketNameRe.MatchString(name) || strings.Contains(name, "..") || ipRe.MatchString(name) {
		return false
	}
	if strings.HasPrefix(name, "xn--") || strings.HasSuffix(name, "-s3alias") {
		return false
	}
	return true
}

func validKey(key string) error {
	if key == "" {
		return newErr("InvalidArgument", "Key objek kosong")
	}
	if len(key) > 1024 {
		return newErr("KeyTooLongError", "Key objek melebihi 1024 byte")
	}
	if strings.ContainsRune(key, 0) {
		return newErr("InvalidArgument", "Key objek berisi karakter tidak valid")
	}
	return nil
}
