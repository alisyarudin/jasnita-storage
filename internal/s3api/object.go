package s3api

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/alisyarudin/jasnita-storage/internal/meta"
	"github.com/alisyarudin/jasnita-storage/internal/sigv4"
)

// standardHeaders adalah header objek yang ikut disimpan selain Content-Type.
var standardHeaders = []string{"Cache-Control", "Content-Disposition", "Content-Encoding", "Content-Language", "Expires"}

func extractMetadata(h http.Header) map[string]string {
	md := map[string]string{}
	for k, vals := range h {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "x-amz-meta-") && len(vals) > 0 {
			md[lk] = vals[0]
		}
	}
	for _, name := range standardHeaders {
		v := h.Get(name)
		if name == "Content-Encoding" && v != "" {
			var parts []string
			for _, p := range strings.Split(v, ",") {
				if p = strings.TrimSpace(p); p != "" && p != "aws-chunked" {
					parts = append(parts, p)
				}
			}
			v = strings.Join(parts, ", ")
		}
		if v != "" {
			md[strings.ToLower(name)] = v
		}
	}
	return md
}

func contentTypeOf(h http.Header) string {
	if ct := h.Get("Content-Type"); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

// bodyReader membungkus body sesuai mode payload SigV4 (aws-chunked bila perlu).
func bodyReader(r *http.Request, info *reqInfo) io.Reader {
	if info.sig != nil && sigv4.IsStreaming(info.sig.ContentSHA256) {
		return sigv4.NewChunkedReader(r.Body, info.sig)
	}
	return r.Body
}

// storeBody menulis body ke blob dan memverifikasi digest. Mengembalikan path blob, ukuran, md5 hex.
func (s *Server) storeBody(r *http.Request, info *reqInfo, bucket string) (blobPath string, size int64, md5hex string, err error) {
	w, err := s.Blob.NewWriter(bucket)
	if err != nil {
		return "", 0, "", err
	}
	if _, err := io.Copy(w, bodyReader(r, info)); err != nil {
		w.Abort()
		if errors.Is(err, sigv4.ErrChunkSignature) {
			return "", 0, "", newErr("SignatureDoesNotMatch", "Tanda tangan chunk tidak cocok")
		}
		return "", 0, "", newErr("IncompleteBody", "Body request tidak lengkap: "+err.Error())
	}
	if info.sig != nil && !info.sig.Presigned {
		sha := info.sig.ContentSHA256
		if len(sha) == 64 && !sigv4.IsStreaming(sha) && !strings.EqualFold(w.SHA256Hex(), sha) {
			w.Abort()
			return "", 0, "", newErr("XAmzContentSHA256Mismatch", "SHA-256 body tidak cocok dengan x-amz-content-sha256")
		}
	}
	if cmd5 := r.Header.Get("Content-MD5"); cmd5 != "" {
		want, err := base64.StdEncoding.DecodeString(cmd5)
		if err != nil || hex.EncodeToString(want) != w.MD5Hex() {
			w.Abort()
			return "", 0, "", newErr("BadDigest", "Content-MD5 tidak cocok")
		}
	}
	path, err := w.Commit()
	if err != nil {
		return "", 0, "", err
	}
	return path, w.Size(), w.MD5Hex(), nil
}

func (s *Server) putObject(ctx context.Context, w http.ResponseWriter, r *http.Request, info *reqInfo) error {
	if err := validKey(info.object); err != nil {
		return err
	}
	if _, err := s.requireWrite(ctx, info); err != nil {
		return err
	}
	path, size, etag, err := s.storeBody(r, info, info.bucket)
	if err != nil {
		return err
	}
	old, err := s.Meta.PutObject(ctx, meta.Object{
		Bucket: info.bucket, Key: info.object, Size: size, ETag: etag,
		ContentType: contentTypeOf(r.Header), Metadata: extractMetadata(r.Header), Blob: path, ModTime: time.Now(),
	})
	if err != nil {
		s.Blob.Remove(path)
		return err
	}
	if old != "" {
		s.Blob.Remove(old)
	}
	w.Header().Set("ETag", `"`+etag+`"`)
	w.WriteHeader(http.StatusOK)
	return nil
}

func setObjectHeaders(h http.Header, o *meta.Object) {
	h.Set("Content-Type", o.ContentType)
	h.Set("ETag", `"`+o.ETag+`"`)
	h.Set("Last-Modified", o.ModTime.UTC().Format(http.TimeFormat))
	for k, v := range o.Metadata {
		h.Set(k, v)
	}
}

var responseOverrides = map[string]string{
	"response-content-type":        "Content-Type",
	"response-content-disposition": "Content-Disposition",
	"response-cache-control":       "Cache-Control",
	"response-content-encoding":    "Content-Encoding",
	"response-content-language":    "Content-Language",
	"response-expires":             "Expires",
}

func (s *Server) getObject(ctx context.Context, w http.ResponseWriter, r *http.Request, info *reqInfo) error {
	if err := validKey(info.object); err != nil {
		return err
	}
	if _, err := s.requireRead(ctx, info, true); err != nil {
		return err
	}
	o, err := s.Meta.GetObject(ctx, info.bucket, info.object)
	if errors.Is(err, meta.ErrNotFound) {
		return errNoSuchKey
	}
	if err != nil {
		return err
	}
	f, err := s.Blob.Open(o.Blob)
	if err != nil {
		s.Log.Error("blob hilang", "bucket", o.Bucket, "key", o.Key, "blob", o.Blob)
		return errNoSuchKey
	}
	defer f.Close()
	h := w.Header()
	setObjectHeaders(h, o)
	q := r.URL.Query()
	for param, header := range responseOverrides {
		if v := q.Get(param); v != "" {
			h.Set(header, v)
		}
	}
	h.Set("X-Amz-Storage-Class", "STANDARD")
	if idx := strings.LastIndex(o.ETag, "-"); idx > 0 {
		h.Set("X-Amz-Mp-Parts-Count", o.ETag[idx+1:])
	}
	// ServeContent menangani Range, If-None-Match, If-Modified-Since, HEAD.
	http.ServeContent(w, r, "", o.ModTime, f)
	return nil
}

func (s *Server) deleteObject(ctx context.Context, w http.ResponseWriter, info *reqInfo) error {
	if err := validKey(info.object); err != nil {
		return err
	}
	if _, err := s.requireWrite(ctx, info); err != nil {
		return err
	}
	blobPath, _, err := s.Meta.DeleteObject(ctx, info.bucket, info.object)
	if err != nil {
		return err
	}
	s.Blob.Remove(blobPath)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type deleteReqXML struct {
	XMLName xml.Name `xml:"Delete"`
	Quiet   bool     `xml:"Quiet"`
	Objects []struct {
		Key string `xml:"Key"`
	} `xml:"Object"`
}

type deleteResXML struct {
	XMLName xml.Name `xml:"DeleteResult"`
	NS      string   `xml:"xmlns,attr"`
	Deleted []struct {
		Key string `xml:"Key"`
	} `xml:"Deleted"`
	Errors []struct {
		Key     string `xml:"Key"`
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	} `xml:"Error"`
}

func (s *Server) deleteObjects(ctx context.Context, w http.ResponseWriter, r *http.Request, info *reqInfo) error {
	if _, err := s.requireWrite(ctx, info); err != nil {
		return err
	}
	var req deleteReqXML
	if err := xml.NewDecoder(io.LimitReader(bodyReader(r, info), 2<<20)).Decode(&req); err != nil {
		return newErr("MalformedXML", "Body DeleteObjects tidak valid")
	}
	if len(req.Objects) > 1000 {
		return newErr("MalformedXML", "Maksimal 1000 key per request")
	}
	out := deleteResXML{NS: xmlNS}
	for _, o := range req.Objects {
		if err := validKey(o.Key); err != nil {
			out.Errors = append(out.Errors, struct {
				Key     string `xml:"Key"`
				Code    string `xml:"Code"`
				Message string `xml:"Message"`
			}{o.Key, "InvalidArgument", err.Error()})
			continue
		}
		blobPath, _, err := s.Meta.DeleteObject(ctx, info.bucket, o.Key)
		if err != nil {
			out.Errors = append(out.Errors, struct {
				Key     string `xml:"Key"`
				Code    string `xml:"Code"`
				Message string `xml:"Message"`
			}{o.Key, "InternalError", err.Error()})
			continue
		}
		s.Blob.Remove(blobPath)
		if !req.Quiet {
			out.Deleted = append(out.Deleted, struct {
				Key string `xml:"Key"`
			}{o.Key})
		}
	}
	s.writeXMLOK(w, out)
	return nil
}

type copyResXML struct {
	XMLName      xml.Name `xml:"CopyObjectResult"`
	NS           string   `xml:"xmlns,attr"`
	LastModified string   `xml:"LastModified"`
	ETag         string   `xml:"ETag"`
}

func parseCopySource(v string) (bucket, key string, ok bool) {
	if i := strings.Index(v, "?"); i >= 0 {
		v = v[:i]
	}
	if u, err := url.PathUnescape(v); err == nil {
		v = u
	}
	v = strings.TrimPrefix(v, "/")
	bucket, key, ok = strings.Cut(v, "/")
	return bucket, key, ok && bucket != "" && key != ""
}

func (s *Server) copyObject(ctx context.Context, w http.ResponseWriter, r *http.Request, info *reqInfo) error {
	if err := validKey(info.object); err != nil {
		return err
	}
	if _, err := s.requireWrite(ctx, info); err != nil {
		return err
	}
	srcBucket, srcKey, ok := parseCopySource(r.Header.Get("X-Amz-Copy-Source"))
	if !ok {
		return newErr("InvalidArgument", "x-amz-copy-source tidak valid")
	}
	srcInfo := &reqInfo{key: info.key, bucket: srcBucket}
	if _, err := s.requireRead(ctx, srcInfo, true); err != nil {
		return err
	}
	src, err := s.Meta.GetObject(ctx, srcBucket, srcKey)
	if errors.Is(err, meta.ErrNotFound) {
		return errNoSuchKey
	}
	if err != nil {
		return err
	}
	replace := strings.EqualFold(r.Header.Get("X-Amz-Metadata-Directive"), "REPLACE")
	same := srcBucket == info.bucket && srcKey == info.object
	if same && !replace {
		return newErr("InvalidRequest", "Menyalin objek ke dirinya sendiri tanpa mengubah metadata tidak diizinkan")
	}
	dst := meta.Object{Bucket: info.bucket, Key: info.object, Size: src.Size, ETag: src.ETag, ContentType: src.ContentType, Metadata: src.Metadata, ModTime: time.Now()}
	if replace {
		dst.ContentType = contentTypeOf(r.Header)
		dst.Metadata = extractMetadata(r.Header)
	}
	if same {
		dst.Blob = src.Blob
	} else {
		path, size, md5hex, err := s.Blob.Copy(src.Blob, info.bucket)
		if err != nil {
			return err
		}
		dst.Blob, dst.Size = path, size
		if !strings.Contains(src.ETag, "-") {
			dst.ETag = md5hex
		}
	}
	old, err := s.Meta.PutObject(ctx, dst)
	if err != nil {
		if !same {
			s.Blob.Remove(dst.Blob)
		}
		return err
	}
	if old != "" && old != dst.Blob {
		s.Blob.Remove(old)
	}
	s.writeXMLOK(w, copyResXML{NS: xmlNS, LastModified: dst.ModTime.UTC().Format(iso8601), ETag: `"` + dst.ETag + `"`})
	return nil
}

// md5Of membantu tes & multipart.
func md5Of(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}
