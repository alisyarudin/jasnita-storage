package s3api

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/alisyarudin/jasnita-storage/internal/meta"
)

func newUploadID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

type initiateXML struct {
	XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
	NS       string   `xml:"xmlns,attr"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	UploadID string   `xml:"UploadId"`
}

func (s *Server) initiateMultipart(ctx context.Context, w http.ResponseWriter, r *http.Request, info *reqInfo) error {
	if err := validKey(info.object); err != nil {
		return err
	}
	if _, err := s.requireWrite(ctx, info); err != nil {
		return err
	}
	u := meta.Upload{ID: newUploadID(), Bucket: info.bucket, Key: info.object, ContentType: contentTypeOf(r.Header), Metadata: extractMetadata(r.Header), CreatedAt: time.Now()}
	if err := s.Meta.CreateUpload(ctx, u); err != nil {
		return err
	}
	s.writeXMLOK(w, initiateXML{NS: xmlNS, Bucket: u.Bucket, Key: u.Key, UploadID: u.ID})
	return nil
}

func (s *Server) loadUpload(ctx context.Context, info *reqInfo, id string) (*meta.Upload, error) {
	u, err := s.Meta.GetUpload(ctx, info.bucket, info.object, id)
	if errors.Is(err, meta.ErrNotFound) {
		return nil, errNoSuchUpload
	}
	return u, err
}

func (s *Server) uploadPart(ctx context.Context, w http.ResponseWriter, r *http.Request, info *reqInfo) error {
	if _, err := s.requireWrite(ctx, info); err != nil {
		return err
	}
	q := r.URL.Query()
	n, err := strconv.Atoi(q.Get("partNumber"))
	if err != nil || n < 1 || n > 10000 {
		return newErr("InvalidArgument", "partNumber harus 1..10000")
	}
	u, err := s.loadUpload(ctx, info, q.Get("uploadId"))
	if err != nil {
		return err
	}
	if r.Header.Get("X-Amz-Copy-Source") != "" {
		return newErr("NotImplemented", "UploadPartCopy belum didukung")
	}
	path, size, etag, err := s.storeBody(r, info, info.bucket)
	if err != nil {
		return err
	}
	old, err := s.Meta.PutPart(ctx, meta.Part{UploadID: u.ID, Number: n, Size: size, ETag: etag, Blob: path, ModTime: time.Now()})
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

type completeReqXML struct {
	XMLName xml.Name `xml:"CompleteMultipartUpload"`
	Parts   []struct {
		PartNumber int    `xml:"PartNumber"`
		ETag       string `xml:"ETag"`
	} `xml:"Part"`
}

type completeResXML struct {
	XMLName  xml.Name `xml:"CompleteMultipartUploadResult"`
	NS       string   `xml:"xmlns,attr"`
	Location string   `xml:"Location"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	ETag     string   `xml:"ETag"`
}

func (s *Server) completeMultipart(ctx context.Context, w http.ResponseWriter, r *http.Request, info *reqInfo) error {
	if _, err := s.requireWrite(ctx, info); err != nil {
		return err
	}
	u, err := s.loadUpload(ctx, info, r.URL.Query().Get("uploadId"))
	if err != nil {
		return err
	}
	var req completeReqXML
	if err := xml.NewDecoder(io.LimitReader(bodyReader(r, info), 4<<20)).Decode(&req); err != nil {
		return newErr("MalformedXML", "Body CompleteMultipartUpload tidak valid")
	}
	if len(req.Parts) == 0 {
		return newErr("MalformedXML", "Daftar part kosong")
	}
	stored, err := s.Meta.ListParts(ctx, u.ID)
	if err != nil {
		return err
	}
	byNum := map[int]meta.Part{}
	for _, p := range stored {
		byNum[p.Number] = p
	}
	var ordered []meta.Part
	last := 0
	for _, rp := range req.Parts {
		if rp.PartNumber <= last {
			return newErr("InvalidPartOrder", "Part harus berurutan naik")
		}
		last = rp.PartNumber
		p, ok := byNum[rp.PartNumber]
		if !ok || !strings.EqualFold(strings.Trim(rp.ETag, `"`), p.ETag) {
			return newErr("InvalidPart", "Part "+strconv.Itoa(rp.PartNumber)+" tidak ditemukan atau ETag tidak cocok")
		}
		ordered = append(ordered, p)
	}

	bw, err := s.Blob.NewWriter(info.bucket)
	if err != nil {
		return err
	}
	md5s := md5.New()
	for _, p := range ordered {
		f, err := s.Blob.Open(p.Blob)
		if err != nil {
			bw.Abort()
			return newErr("InvalidPart", "Berkas part "+strconv.Itoa(p.Number)+" hilang")
		}
		_, err = io.Copy(bw, f)
		f.Close()
		if err != nil {
			bw.Abort()
			return err
		}
		raw, _ := hex.DecodeString(p.ETag)
		md5s.Write(raw)
	}
	path, err := bw.Commit()
	if err != nil {
		return err
	}
	etag := hex.EncodeToString(md5s.Sum(nil)) + "-" + strconv.Itoa(len(ordered))
	old, err := s.Meta.PutObject(ctx, meta.Object{
		Bucket: info.bucket, Key: info.object, Size: bw.Size(), ETag: etag,
		ContentType: u.ContentType, Metadata: u.Metadata, Blob: path, ModTime: time.Now(),
	})
	if err != nil {
		s.Blob.Remove(path)
		return err
	}
	if old != "" {
		s.Blob.Remove(old)
	}
	blobs, err := s.Meta.DeleteUpload(ctx, u.ID)
	if err != nil {
		s.Log.Warn("hapus upload setelah complete", "upload", u.ID, "err", err)
	}
	for _, b := range blobs {
		s.Blob.Remove(b)
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	s.writeXMLOK(w, completeResXML{
		NS: xmlNS, Location: scheme + "://" + r.Host + "/" + info.bucket + "/" + info.object,
		Bucket: info.bucket, Key: info.object, ETag: `"` + etag + `"`,
	})
	return nil
}

func (s *Server) abortMultipart(ctx context.Context, w http.ResponseWriter, r *http.Request, info *reqInfo) error {
	if _, err := s.requireWrite(ctx, info); err != nil {
		return err
	}
	u, err := s.loadUpload(ctx, info, r.URL.Query().Get("uploadId"))
	if err != nil {
		return err
	}
	if err := s.AbortUpload(ctx, u.ID); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// AbortUpload menghapus upload beserta part-nya dari metadata dan disk.
func (s *Server) AbortUpload(ctx context.Context, id string) error {
	blobs, err := s.Meta.DeleteUpload(ctx, id)
	if err != nil {
		return err
	}
	for _, b := range blobs {
		s.Blob.Remove(b)
	}
	return nil
}

type listPartsXML struct {
	XMLName              xml.Name `xml:"ListPartsResult"`
	NS                   string   `xml:"xmlns,attr"`
	Bucket               string   `xml:"Bucket"`
	Key                  string   `xml:"Key"`
	UploadID             string   `xml:"UploadId"`
	StorageClass         string   `xml:"StorageClass"`
	PartNumberMarker     int      `xml:"PartNumberMarker"`
	NextPartNumberMarker int      `xml:"NextPartNumberMarker"`
	MaxParts             int      `xml:"MaxParts"`
	IsTruncated          bool     `xml:"IsTruncated"`
	Parts                []struct {
		PartNumber   int    `xml:"PartNumber"`
		LastModified string `xml:"LastModified"`
		ETag         string `xml:"ETag"`
		Size         int64  `xml:"Size"`
	} `xml:"Part"`
}

func (s *Server) listParts(ctx context.Context, w http.ResponseWriter, r *http.Request, info *reqInfo) error {
	if _, err := s.requireRead(ctx, info, false); err != nil {
		return err
	}
	u, err := s.loadUpload(ctx, info, r.URL.Query().Get("uploadId"))
	if err != nil {
		return err
	}
	parts, err := s.Meta.ListParts(ctx, u.ID)
	if err != nil {
		return err
	}
	out := listPartsXML{NS: xmlNS, Bucket: u.Bucket, Key: u.Key, UploadID: u.ID, StorageClass: "STANDARD", MaxParts: 10000}
	for _, p := range parts {
		out.Parts = append(out.Parts, struct {
			PartNumber   int    `xml:"PartNumber"`
			LastModified string `xml:"LastModified"`
			ETag         string `xml:"ETag"`
			Size         int64  `xml:"Size"`
		}{p.Number, p.ModTime.UTC().Format(iso8601), `"` + p.ETag + `"`, p.Size})
		out.NextPartNumberMarker = p.Number
	}
	s.writeXMLOK(w, out)
	return nil
}

type listUploadsXML struct {
	XMLName     xml.Name `xml:"ListMultipartUploadsResult"`
	NS          string   `xml:"xmlns,attr"`
	Bucket      string   `xml:"Bucket"`
	Prefix      string   `xml:"Prefix"`
	KeyMarker   string   `xml:"KeyMarker"`
	MaxUploads  int      `xml:"MaxUploads"`
	IsTruncated bool     `xml:"IsTruncated"`
	Uploads     []struct {
		Key          string `xml:"Key"`
		UploadID     string `xml:"UploadId"`
		StorageClass string `xml:"StorageClass"`
		Initiated    string `xml:"Initiated"`
	} `xml:"Upload"`
}

func (s *Server) listMultipartUploads(ctx context.Context, w http.ResponseWriter, r *http.Request, info *reqInfo) error {
	if _, err := s.requireRead(ctx, info, false); err != nil {
		return err
	}
	prefix := r.URL.Query().Get("prefix")
	ups, err := s.Meta.ListUploads(ctx, info.bucket, prefix)
	if err != nil {
		return err
	}
	out := listUploadsXML{NS: xmlNS, Bucket: info.bucket, Prefix: prefix, MaxUploads: 1000}
	for _, u := range ups {
		out.Uploads = append(out.Uploads, struct {
			Key          string `xml:"Key"`
			UploadID     string `xml:"UploadId"`
			StorageClass string `xml:"StorageClass"`
			Initiated    string `xml:"Initiated"`
		}{u.Key, u.ID, "STANDARD", u.CreatedAt.UTC().Format(iso8601)})
	}
	s.writeXMLOK(w, out)
	return nil
}

// CleanStaleUploads membatalkan upload multipart yang lebih tua dari maxAge.
func (s *Server) CleanStaleUploads(ctx context.Context, maxAge time.Duration) int {
	ups, err := s.Meta.ListUploadsOlderThan(ctx, time.Now().Add(-maxAge))
	if err != nil {
		s.Log.Warn("daftar upload basi", "err", err)
		return 0
	}
	n := 0
	for _, u := range ups {
		if err := s.AbortUpload(ctx, u.ID); err == nil {
			n++
		}
	}
	return n
}
