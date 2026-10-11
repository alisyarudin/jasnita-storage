package s3api

import (
	"context"
	"io"
	"time"

	"github.com/alisyarudin/jasnita-storage/internal/meta"
)

// StoreObject menyimpan objek dari reader (dipakai console; tanpa SigV4).
func (s *Server) StoreObject(ctx context.Context, bucket, key string, body io.Reader, contentType string, md map[string]string) (*meta.Object, error) {
	if err := validKey(key); err != nil {
		return nil, err
	}
	if _, err := s.loadBucket(ctx, bucket); err != nil {
		return nil, err
	}
	w, err := s.Blob.NewWriter(bucket)
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(w, body); err != nil {
		w.Abort()
		return nil, err
	}
	path, err := w.Commit()
	if err != nil {
		return nil, err
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	o := meta.Object{Bucket: bucket, Key: key, Size: w.Size(), ETag: w.MD5Hex(), ContentType: contentType, Metadata: md, Blob: path, ModTime: time.Now()}
	old, err := s.Meta.PutObject(ctx, o)
	if err != nil {
		s.Blob.Remove(path)
		return nil, err
	}
	if old != "" {
		s.Blob.Remove(old)
	}
	return &o, nil
}

// RemoveObject menghapus satu objek (tanpa error bila tidak ada).
func (s *Server) RemoveObject(ctx context.Context, bucket, key string) error {
	blobPath, _, err := s.Meta.DeleteObject(ctx, bucket, key)
	if err != nil {
		return err
	}
	return s.Blob.Remove(blobPath)
}

// RemovePrefix menghapus semua objek berawalan prefix. Mengembalikan jumlah yang dihapus.
func (s *Server) RemovePrefix(ctx context.Context, bucket, prefix string) (int, error) {
	n := 0
	for {
		entries, truncated, _, err := s.ListEntries(ctx, bucket, prefix, "", "", 1000)
		if err != nil {
			return n, err
		}
		for _, e := range entries {
			if err := s.RemoveObject(ctx, bucket, e.obj.Key); err != nil {
				return n, err
			}
			n++
		}
		if !truncated || len(entries) == 0 {
			return n, nil
		}
	}
}

// Entry adalah hasil listing untuk pemakai luar paket.
type Entry struct {
	Prefix string       `json:"prefix,omitempty"`
	Object *meta.Object `json:"object,omitempty"`
}

// List membungkus ListEntries dengan tipe yang diekspor.
func (s *Server) List(ctx context.Context, bucket, prefix, delimiter, cursor string, maxKeys int) (entries []Entry, truncated bool, next string, err error) {
	raw, truncated, next, err := s.ListEntries(ctx, bucket, prefix, delimiter, cursor, maxKeys)
	if err != nil {
		return nil, false, "", err
	}
	for _, e := range raw {
		entries = append(entries, Entry{Prefix: e.prefix, Object: e.obj})
	}
	return entries, truncated, next, nil
}

// EncodeCursor/DecodeCursor untuk token halaman di API console.
func EncodeCursor(c string) string         { return encodeToken(c) }
func DecodeCursor(t string) (string, bool) { return decodeToken(t) }
