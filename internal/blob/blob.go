// Package blob menyimpan isi objek sebagai berkas di disk.
//
// Tata letak: <data>/<bucket>/<2 hex>/<id>. Nama berkas tidak pernah diturunkan
// dari key objek, sehingga key apa pun (termasuk yang berakhiran "/" atau
// berisi karakter aneh) aman. Pemetaan key -> berkas ada di paket meta.
package blob

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type Store struct {
	root string
}

func New(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, err
	}
	return &Store{root: root}, nil
}

func (s *Store) Root() string { return s.root }

func (s *Store) EnsureBucket(bucket string) error {
	return os.MkdirAll(filepath.Join(s.root, bucket, ".tmp"), 0o750)
}

// RemoveBucket menghapus direktori bucket beserta isinya.
func (s *Store) RemoveBucket(bucket string) error {
	if bucket == "" || strings.ContainsAny(bucket, "/\\") || bucket == "." || bucket == ".." {
		return errors.New("nama bucket tidak valid")
	}
	return os.RemoveAll(filepath.Join(s.root, bucket))
}

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// Writer menulis ke berkas sementara sambil menghitung ukuran, MD5 dan SHA-256.
type Writer struct {
	s      *Store
	bucket string
	f      *os.File
	tmp    string
	size   int64
	md5    hash.Hash
	sha    hash.Hash
	done   bool
}

func (s *Store) NewWriter(bucket string) (*Writer, error) {
	tmpDir := filepath.Join(s.root, bucket, ".tmp")
	if err := os.MkdirAll(tmpDir, 0o750); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(tmpDir, "put-*")
	if err != nil {
		return nil, err
	}
	return &Writer{s: s, bucket: bucket, f: f, tmp: f.Name(), md5: md5.New(), sha: sha256.New()}, nil
}

func (w *Writer) Write(p []byte) (int, error) {
	n, err := w.f.Write(p)
	w.size += int64(n)
	w.md5.Write(p[:n])
	w.sha.Write(p[:n])
	return n, err
}

func (w *Writer) Size() int64       { return w.size }
func (w *Writer) MD5Hex() string    { return hex.EncodeToString(w.md5.Sum(nil)) }
func (w *Writer) MD5Sum() []byte    { return w.md5.Sum(nil) }
func (w *Writer) SHA256Hex() string { return hex.EncodeToString(w.sha.Sum(nil)) }

// Commit memindahkan berkas sementara ke lokasi final dan mengembalikan path
// relatif terhadap root (disimpan di metadata).
func (w *Writer) Commit() (string, error) {
	if w.done {
		return "", errors.New("writer sudah ditutup")
	}
	w.done = true
	if err := w.f.Sync(); err != nil {
		w.f.Close()
		os.Remove(w.tmp)
		return "", err
	}
	if err := w.f.Close(); err != nil {
		os.Remove(w.tmp)
		return "", err
	}
	id := newID()
	rel := filepath.Join(w.bucket, id[:2], id)
	final := filepath.Join(w.s.root, rel)
	if err := os.MkdirAll(filepath.Dir(final), 0o750); err != nil {
		os.Remove(w.tmp)
		return "", err
	}
	if err := os.Rename(w.tmp, final); err != nil {
		os.Remove(w.tmp)
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

// Abort membuang berkas sementara.
func (w *Writer) Abort() {
	if w.done {
		return
	}
	w.done = true
	w.f.Close()
	os.Remove(w.tmp)
}

func (s *Store) Open(rel string) (*os.File, error) {
	return os.Open(filepath.Join(s.root, filepath.FromSlash(rel)))
}

func (s *Store) Remove(rel string) error {
	if rel == "" {
		return nil
	}
	err := os.Remove(filepath.Join(s.root, filepath.FromSlash(rel)))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Copy menyalin isi berkas blob ke blob baru di bucket tujuan.
func (s *Store) Copy(srcRel, dstBucket string) (rel string, size int64, md5hex string, err error) {
	src, err := s.Open(srcRel)
	if err != nil {
		return "", 0, "", err
	}
	defer src.Close()
	w, err := s.NewWriter(dstBucket)
	if err != nil {
		return "", 0, "", err
	}
	if _, err := io.Copy(w, src); err != nil {
		w.Abort()
		return "", 0, "", err
	}
	rel, err = w.Commit()
	if err != nil {
		return "", 0, "", err
	}
	return rel, w.Size(), w.MD5Hex(), nil
}

type DiskUsage struct {
	Total uint64 `json:"total"`
	Free  uint64 `json:"free"`
	Used  uint64 `json:"used"`
}

func (s *Store) DiskUsage() (DiskUsage, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(s.root, &st); err != nil {
		return DiskUsage{}, fmt.Errorf("statfs: %w", err)
	}
	total := st.Blocks * uint64(st.Bsize)
	free := st.Bavail * uint64(st.Bsize)
	return DiskUsage{Total: total, Free: free, Used: total - free}, nil
}
