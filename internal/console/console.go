// Package console menyediakan admin API (JSON) dan melayani web console (Vue, embedded).
package console

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/alisyarudin/jasnita-storage/internal/config"
	"github.com/alisyarudin/jasnita-storage/internal/meta"
	"github.com/alisyarudin/jasnita-storage/internal/s3api"
)

//go:embed all:dist
var distFS embed.FS

const tokenTTL = 12 * time.Hour

type Console struct {
	s3       *s3api.Server
	cfg      *config.Config
	log      *slog.Logger
	version  string
	tokenKey []byte
	mux      *http.ServeMux
	static   http.Handler
}

func New(s3 *s3api.Server, cfg *config.Config, log *slog.Logger, version string) *Console {
	key := sha256.Sum256([]byte("jasstore-console-token:" + cfg.RootSecretKey))
	sub, _ := fs.Sub(distFS, "dist")
	c := &Console{s3: s3, cfg: cfg, log: log, version: version, tokenKey: key[:], mux: http.NewServeMux(), static: http.FileServer(http.FS(sub))}
	c.routes()
	return c
}

func (c *Console) routes() {
	m := c.mux
	m.HandleFunc("POST /api/login", c.login)
	m.HandleFunc("GET /api/config", c.auth(c.getConfig))
	m.HandleFunc("GET /api/me", c.auth(c.me))
	m.HandleFunc("GET /api/stats", c.auth(c.stats))
	m.HandleFunc("GET /api/buckets", c.auth(c.listBuckets))
	m.HandleFunc("POST /api/buckets", c.auth(c.createBucket))
	m.HandleFunc("PATCH /api/buckets/{name}", c.auth(c.updateBucket))
	m.HandleFunc("DELETE /api/buckets/{name}", c.auth(c.deleteBucket))
	m.HandleFunc("GET /api/buckets/{name}/objects", c.auth(c.listObjects))
	m.HandleFunc("PUT /api/buckets/{name}/object", c.auth(c.uploadObject))
	m.HandleFunc("GET /api/buckets/{name}/object", c.auth(c.getObject))
	m.HandleFunc("DELETE /api/buckets/{name}/object", c.auth(c.deleteObject))
	m.HandleFunc("POST /api/buckets/{name}/folder", c.auth(c.createFolder))
	m.HandleFunc("GET /api/keys", c.auth(c.listKeys))
	m.HandleFunc("POST /api/keys", c.auth(c.createKey))
	m.HandleFunc("PATCH /api/keys/{ak}", c.auth(c.updateKey))
	m.HandleFunc("DELETE /api/keys/{ak}", c.auth(c.deleteKey))
	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { jsonError(w, 404, "endpoint tidak ada") })
}

func (c *Console) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		c.mux.ServeHTTP(w, r)
		return
	}
	// SPA: berkas statis bila ada, selain itu index.html
	p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if p == "" {
		p = "index.html"
	}
	if f, err := distFS.Open("dist/" + p); err == nil {
		f.Close()
		if p != "index.html" && strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		c.static.ServeHTTP(w, r)
		return
	}
	r.URL.Path = "/"
	w.Header().Set("Cache-Control", "no-cache")
	c.static.ServeHTTP(w, r)
}

// ---------- util ----------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v)
}

// ---------- token ----------

type tokenPayload struct {
	AK  string `json:"ak"`
	Exp int64  `json:"exp"`
}

func (c *Console) issueToken(ak string) string {
	b, _ := json.Marshal(tokenPayload{AK: ak, Exp: time.Now().Add(tokenTTL).Unix()})
	p := base64.RawURLEncoding.EncodeToString(b)
	mac := hmac.New(sha256.New, c.tokenKey)
	mac.Write([]byte(p))
	return p + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (c *Console) parseToken(tok string) (string, bool) {
	p, sig, ok := strings.Cut(tok, ".")
	if !ok {
		return "", false
	}
	mac := hmac.New(sha256.New, c.tokenKey)
	mac.Write([]byte(p))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(want), []byte(sig)) != 1 {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil {
		return "", false
	}
	var tp tokenPayload
	if json.Unmarshal(raw, &tp) != nil || time.Now().Unix() > tp.Exp {
		return "", false
	}
	return tp.AK, true
}

type handler func(w http.ResponseWriter, r *http.Request, k *meta.AccessKey)

func (c *Console) auth(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if tok == "" {
			tok = r.URL.Query().Get("token") // untuk link unduh
		}
		ak, ok := c.parseToken(tok)
		if !ok {
			jsonError(w, 401, "sesi tidak valid atau kedaluwarsa")
			return
		}
		k, err := c.s3.Meta.GetAccessKey(r.Context(), ak)
		if err != nil || !k.Enabled {
			jsonError(w, 401, "access key tidak aktif")
			return
		}
		h(w, r, k)
	}
}

// ---------- auth & info ----------

func (c *Console) login(w http.ResponseWriter, r *http.Request) {
	var in struct{ AccessKey, SecretKey string }
	if err := readJSON(r, &in); err != nil {
		jsonError(w, 400, "body tidak valid")
		return
	}
	k, err := c.s3.Meta.GetAccessKey(r.Context(), in.AccessKey)
	if err != nil || !k.Enabled || subtle.ConstantTimeCompare([]byte(k.SecretKey), []byte(in.SecretKey)) != 1 {
		time.Sleep(300 * time.Millisecond)
		jsonError(w, 401, "access key atau secret key salah")
		return
	}
	writeJSON(w, 200, map[string]any{"token": c.issueToken(k.AccessKey), "user": k})
}

func (c *Console) me(w http.ResponseWriter, r *http.Request, k *meta.AccessKey) {
	writeJSON(w, 200, k)
}

func (c *Console) getConfig(w http.ResponseWriter, r *http.Request, k *meta.AccessKey) {
	endpoint := c.cfg.PublicURL
	if endpoint == "" {
		host := r.Host
		if h, _, ok := strings.Cut(host, ":"); ok {
			host = h
		}
		port := strings.TrimPrefix(c.cfg.S3Addr, ":")
		if i := strings.LastIndex(c.cfg.S3Addr, ":"); i >= 0 {
			port = c.cfg.S3Addr[i+1:]
		}
		endpoint = "http://" + host + ":" + port
	}
	writeJSON(w, 200, map[string]any{"endpoint": endpoint, "region": c.cfg.Region, "version": c.version, "domain": c.cfg.Domain})
}

func (c *Console) stats(w http.ResponseWriter, r *http.Request, k *meta.AccessKey) {
	buckets, st, err := c.s3.Meta.TotalStats(r.Context())
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	du, _ := c.s3.Blob.DiskUsage()
	writeJSON(w, 200, map[string]any{"buckets": buckets, "objects": st.Objects, "bytes": st.Bytes, "disk": du})
}

// ---------- buckets ----------

type bucketOut struct {
	meta.Bucket
	meta.BucketStats
}

func (c *Console) listBuckets(w http.ResponseWriter, r *http.Request, k *meta.AccessKey) {
	all, err := c.s3.Meta.ListBuckets(r.Context())
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	out := []bucketOut{}
	for _, b := range all {
		if !k.CanRead(b.Name) {
			continue
		}
		st, _ := c.s3.Meta.BucketStats(r.Context(), b.Name)
		out = append(out, bucketOut{b, st})
	}
	writeJSON(w, 200, out)
}

func (c *Console) createBucket(w http.ResponseWriter, r *http.Request, k *meta.AccessKey) {
	var in struct {
		Name   string `json:"name"`
		Public bool   `json:"public"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, 400, "body tidak valid")
		return
	}
	if !k.CanCreateBucket() {
		jsonError(w, 403, "tidak punya izin membuat bucket")
		return
	}
	if !s3api.ValidBucketName(in.Name) {
		jsonError(w, 400, "nama bucket tidak valid: 3-63 karakter, huruf kecil, angka, titik, strip")
		return
	}
	err := c.s3.Meta.CreateBucket(r.Context(), meta.Bucket{Name: in.Name, Owner: k.AccessKey, Public: in.Public, CreatedAt: time.Now()})
	if errors.Is(err, meta.ErrExists) {
		jsonError(w, 409, "bucket sudah ada")
		return
	}
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	c.s3.Blob.EnsureBucket(in.Name)
	b, _ := c.s3.Meta.GetBucket(r.Context(), in.Name)
	writeJSON(w, 201, b)
}

func (c *Console) updateBucket(w http.ResponseWriter, r *http.Request, k *meta.AccessKey) {
	name := r.PathValue("name")
	if !k.CanWrite(name) {
		jsonError(w, 403, "tidak punya izin")
		return
	}
	var in struct {
		Public *bool `json:"public"`
	}
	if err := readJSON(r, &in); err != nil || in.Public == nil {
		jsonError(w, 400, "body tidak valid")
		return
	}
	if err := c.s3.Meta.SetBucketPublic(r.Context(), name, *in.Public); err != nil {
		jsonError(w, 404, "bucket tidak ada")
		return
	}
	b, _ := c.s3.Meta.GetBucket(r.Context(), name)
	writeJSON(w, 200, b)
}

func (c *Console) deleteBucket(w http.ResponseWriter, r *http.Request, k *meta.AccessKey) {
	name := r.PathValue("name")
	force := r.URL.Query().Get("force") == "1"
	if !k.CanWrite(name) || (force && !k.Admin) {
		jsonError(w, 403, "tidak punya izin")
		return
	}
	var err error
	if force {
		err = c.s3.DeleteBucketForce(r.Context(), name)
	} else {
		err = c.s3.Meta.DeleteBucket(r.Context(), name)
		if err == nil {
			c.s3.Blob.RemoveBucket(name)
		}
	}
	switch {
	case errors.Is(err, meta.ErrNotEmpty):
		jsonError(w, 409, "bucket belum kosong")
	case errors.Is(err, meta.ErrNotFound):
		jsonError(w, 404, "bucket tidak ada")
	case err != nil:
		jsonError(w, 500, err.Error())
	default:
		w.WriteHeader(204)
	}
}

// ---------- objek ----------

func (c *Console) listObjects(w http.ResponseWriter, r *http.Request, k *meta.AccessKey) {
	name := r.PathValue("name")
	if !k.CanRead(name) {
		jsonError(w, 403, "tidak punya izin")
		return
	}
	if _, err := c.s3.Meta.GetBucket(r.Context(), name); err != nil {
		jsonError(w, 404, "bucket tidak ada")
		return
	}
	q := r.URL.Query()
	prefix := q.Get("prefix")
	delimiter := "/"
	if q.Get("recursive") == "1" {
		delimiter = ""
	}
	cursor := ""
	if t := q.Get("token"); t != "" {
		cursor, _ = s3api.DecodeCursor(t)
	}
	limit := 500
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && n <= 1000 {
		limit = n
	}
	entries, truncated, next, err := c.s3.List(r.Context(), name, prefix, delimiter, cursor, limit)
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	type item struct {
		Key         string    `json:"key"`
		Name        string    `json:"name"`
		Dir         bool      `json:"dir"`
		Size        int64     `json:"size"`
		ETag        string    `json:"etag,omitempty"`
		ContentType string    `json:"contentType,omitempty"`
		Modified    time.Time `json:"modified"`
	}
	items := []item{}
	for _, e := range entries {
		if e.Prefix != "" {
			items = append(items, item{Key: e.Prefix, Name: strings.TrimSuffix(strings.TrimPrefix(e.Prefix, prefix), "/"), Dir: true})
			continue
		}
		o := e.Object
		if o.Key == prefix && strings.HasSuffix(o.Key, "/") {
			continue // penanda folder itu sendiri
		}
		items = append(items, item{Key: o.Key, Name: strings.TrimPrefix(o.Key, prefix), Size: o.Size, ETag: o.ETag, ContentType: o.ContentType, Modified: o.ModTime})
	}
	out := map[string]any{"items": items, "truncated": truncated}
	if truncated {
		out["next"] = s3api.EncodeCursor(next)
	}
	writeJSON(w, 200, out)
}

func (c *Console) uploadObject(w http.ResponseWriter, r *http.Request, k *meta.AccessKey) {
	name := r.PathValue("name")
	key := r.URL.Query().Get("key")
	if !k.CanWrite(name) {
		jsonError(w, 403, "tidak punya izin menulis")
		return
	}
	ct := r.Header.Get("Content-Type")
	o, err := c.s3.StoreObject(r.Context(), name, key, r.Body, ct, nil)
	if err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, o)
}

func (c *Console) getObject(w http.ResponseWriter, r *http.Request, k *meta.AccessKey) {
	name := r.PathValue("name")
	key := r.URL.Query().Get("key")
	if !k.CanRead(name) {
		jsonError(w, 403, "tidak punya izin")
		return
	}
	o, err := c.s3.Meta.GetObject(r.Context(), name, key)
	if err != nil {
		jsonError(w, 404, "objek tidak ada")
		return
	}
	f, err := c.s3.Blob.Open(o.Blob)
	if err != nil {
		jsonError(w, 404, "berkas objek hilang")
		return
	}
	defer f.Close()
	h := w.Header()
	ct := o.ContentType
	if ct == "" || ct == "application/octet-stream" || ct == "binary/octet-stream" {
		if guess := mime.TypeByExtension(strings.ToLower(path.Ext(key))); guess != "" {
			ct = guess
		}
	}
	h.Set("Content-Type", ct)
	h.Set("ETag", `"`+o.ETag+`"`)
	base := key
	if i := strings.LastIndex(key, "/"); i >= 0 {
		base = key[i+1:]
	}
	disp := "inline"
	if r.URL.Query().Get("download") == "1" {
		disp = "attachment"
	}
	h.Set("Content-Disposition", disp+`; filename="`+strings.ReplaceAll(base, `"`, "")+`"`)
	http.ServeContent(w, r, "", o.ModTime, f)
}

func (c *Console) deleteObject(w http.ResponseWriter, r *http.Request, k *meta.AccessKey) {
	name := r.PathValue("name")
	if !k.CanWrite(name) {
		jsonError(w, 403, "tidak punya izin menulis")
		return
	}
	q := r.URL.Query()
	if prefix := q.Get("prefix"); prefix != "" {
		n, err := c.s3.RemovePrefix(r.Context(), name, prefix)
		if err != nil {
			jsonError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]int{"deleted": n})
		return
	}
	if err := c.s3.RemoveObject(r.Context(), name, q.Get("key")); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	w.WriteHeader(204)
}

func (c *Console) createFolder(w http.ResponseWriter, r *http.Request, k *meta.AccessKey) {
	name := r.PathValue("name")
	if !k.CanWrite(name) {
		jsonError(w, 403, "tidak punya izin menulis")
		return
	}
	var in struct {
		Prefix string `json:"prefix"`
	}
	if err := readJSON(r, &in); err != nil || strings.Trim(in.Prefix, "/") == "" {
		jsonError(w, 400, "nama folder kosong")
		return
	}
	key := strings.Trim(in.Prefix, "/") + "/"
	if _, err := c.s3.StoreObject(r.Context(), name, key, strings.NewReader(""), "application/x-directory", nil); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, map[string]string{"key": key})
}

// ---------- access keys ----------

func (c *Console) listKeys(w http.ResponseWriter, r *http.Request, k *meta.AccessKey) {
	if !k.Admin {
		writeJSON(w, 200, []meta.AccessKey{*k})
		return
	}
	keys, err := c.s3.Meta.ListAccessKeys(r.Context())
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	if keys == nil {
		keys = []meta.AccessKey{}
	}
	writeJSON(w, 200, keys)
}

type keyInput struct {
	Name     *string       `json:"name"`
	Admin    *bool         `json:"admin"`
	Enabled  *bool         `json:"enabled"`
	Policies []meta.Policy `json:"policies"`
}

func validPolicies(ps []meta.Policy) bool {
	for _, p := range ps {
		if p.Permission != "read" && p.Permission != "readwrite" {
			return false
		}
		if p.Bucket != "*" && !s3api.ValidBucketName(p.Bucket) {
			return false
		}
	}
	return true
}

func (c *Console) createKey(w http.ResponseWriter, r *http.Request, k *meta.AccessKey) {
	if !k.Admin {
		jsonError(w, 403, "hanya admin")
		return
	}
	var in keyInput
	if err := readJSON(r, &in); err != nil || !validPolicies(in.Policies) {
		jsonError(w, 400, "body tidak valid")
		return
	}
	nk := meta.AccessKey{AccessKey: meta.NewAccessKeyID(), SecretKey: meta.NewSecretKey(), Enabled: true, Policies: in.Policies, CreatedAt: time.Now()}
	if in.Name != nil {
		nk.Name = *in.Name
	}
	if in.Admin != nil {
		nk.Admin = *in.Admin
	}
	if nk.Policies == nil {
		nk.Policies = []meta.Policy{}
	}
	if err := c.s3.Meta.CreateAccessKey(r.Context(), nk); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"key": nk, "secretKey": nk.SecretKey})
}

func (c *Console) updateKey(w http.ResponseWriter, r *http.Request, k *meta.AccessKey) {
	ak := r.PathValue("ak")
	if !k.Admin {
		jsonError(w, 403, "hanya admin")
		return
	}
	var in keyInput
	if err := readJSON(r, &in); err != nil || !validPolicies(in.Policies) {
		jsonError(w, 400, "body tidak valid")
		return
	}
	if ak == c.cfg.RootAccessKey && ((in.Enabled != nil && !*in.Enabled) || (in.Admin != nil && !*in.Admin)) {
		jsonError(w, 400, "root key tidak bisa dinonaktifkan")
		return
	}
	if err := c.s3.Meta.UpdateAccessKey(r.Context(), ak, in.Name, in.Policies, in.Enabled, in.Admin); err != nil {
		jsonError(w, 404, "access key tidak ada")
		return
	}
	nk, _ := c.s3.Meta.GetAccessKey(r.Context(), ak)
	writeJSON(w, 200, nk)
}

func (c *Console) deleteKey(w http.ResponseWriter, r *http.Request, k *meta.AccessKey) {
	ak := r.PathValue("ak")
	if !k.Admin {
		jsonError(w, 403, "hanya admin")
		return
	}
	if ak == c.cfg.RootAccessKey || ak == k.AccessKey {
		jsonError(w, 400, "root key / key sendiri tidak bisa dihapus")
		return
	}
	if err := c.s3.Meta.DeleteAccessKey(r.Context(), ak); err != nil {
		jsonError(w, 404, "access key tidak ada")
		return
	}
	w.WriteHeader(204)
}
