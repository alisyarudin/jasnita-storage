package s3api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alisyarudin/jasnita-storage/internal/blob"
	"github.com/alisyarudin/jasnita-storage/internal/meta"
	"github.com/alisyarudin/jasnita-storage/internal/sigv4"
)

const (
	adminAK = "ADMINKEY"
	adminSK = "adminsecret-adminsecret"
	userAK  = "USERKEY"
	userSK  = "usersecret-usersecret"
)

type env struct {
	t   *testing.T
	srv *httptest.Server
	s   *Server
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	m, err := meta.Open(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	b, err := blob.New(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	m.UpsertAccessKey(ctx, meta.AccessKey{AccessKey: adminAK, SecretKey: adminSK, Admin: true, Enabled: true})
	m.UpsertAccessKey(ctx, meta.AccessKey{AccessKey: userAK, SecretKey: userSK, Enabled: true, Policies: []meta.Policy{{Bucket: "shared", Permission: "read"}}})
	s := &Server{Meta: m, Blob: b, Region: "us-east-1", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return &env{t: t, srv: srv, s: s}
}

type resp struct {
	code int
	hdr  http.Header
	body []byte
}

func (e *env) do(ak, sk, method, path string, body []byte, hdr map[string]string) resp {
	e.t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, bytes.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	var extra []string
	for k, v := range hdr {
		req.Header.Set(k, v)
		extra = append(extra, k)
	}
	if ak != "" {
		sum := sha256.Sum256(body)
		sigv4.Sign(req, ak, sk, "us-east-1", time.Now(), hex.EncodeToString(sum[:]), extra...)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{res.StatusCode, res.Header, b}
}

func (e *env) must(r resp, code int, what string) resp {
	e.t.Helper()
	if r.code != code {
		e.t.Fatalf("%s: status %d, ingin %d: %s", what, r.code, code, r.body)
	}
	return r
}

func TestBucketAndObjectLifecycle(t *testing.T) {
	e := newEnv(t)
	e.must(e.do(adminAK, adminSK, "PUT", "/Bad_Name", nil, nil), 400, "nama bucket")
	e.must(e.do(adminAK, adminSK, "PUT", "/docs", nil, nil), 200, "buat bucket")
	e.must(e.do(adminAK, adminSK, "PUT", "/docs", nil, nil), 409, "bucket ganda")
	e.must(e.do(adminAK, adminSK, "HEAD", "/docs", nil, nil), 200, "head bucket")
	e.must(e.do(adminAK, adminSK, "HEAD", "/nope", nil, nil), 404, "head bucket hilang")
	e.must(e.do("", "", "GET", "/", nil, nil), 403, "list bucket anonim")
	lb := e.must(e.do(adminAK, adminSK, "GET", "/", nil, nil), 200, "list bucket")
	if !strings.Contains(string(lb.body), "<Name>docs</Name>") {
		t.Fatalf("list bucket: %s", lb.body)
	}

	content := []byte("isi berkas percobaan")
	pr := e.must(e.do(adminAK, adminSK, "PUT", "/docs/folder/a%20b.txt", content, map[string]string{"Content-Type": "text/plain", "x-amz-meta-owner": "ali", "Cache-Control": "max-age=60"}), 200, "put")
	if pr.hdr.Get("ETag") != `"`+md5Of(content)+`"` {
		t.Fatalf("etag %s", pr.hdr.Get("ETag"))
	}
	gr := e.must(e.do(adminAK, adminSK, "GET", "/docs/folder/a%20b.txt", nil, nil), 200, "get")
	if !bytes.Equal(gr.body, content) || gr.hdr.Get("Content-Type") != "text/plain" || gr.hdr.Get("X-Amz-Meta-Owner") != "ali" || gr.hdr.Get("Cache-Control") != "max-age=60" {
		t.Fatalf("get: %d %v %s", gr.code, gr.hdr, gr.body)
	}
	hr := e.must(e.do(adminAK, adminSK, "HEAD", "/docs/folder/a%20b.txt", nil, nil), 200, "head")
	if hr.hdr.Get("Content-Length") != fmt.Sprint(len(content)) {
		t.Fatalf("head content-length %s", hr.hdr.Get("Content-Length"))
	}
	rr := e.must(e.do(adminAK, adminSK, "GET", "/docs/folder/a%20b.txt", nil, map[string]string{"Range": "bytes=0-2"}), 206, "range")
	if string(rr.body) != "isi" {
		t.Fatalf("range: %q", rr.body)
	}
	e.must(e.do(adminAK, adminSK, "GET", "/docs/none.txt", nil, nil), 404, "get hilang")
	e.must(e.do("", "", "GET", "/docs/folder/a%20b.txt", nil, nil), 403, "get anonim privat")

	// ganti isi -> blob lama dihapus
	e.must(e.do(adminAK, adminSK, "PUT", "/docs/folder/a%20b.txt", []byte("baru"), map[string]string{"x-amz-meta-owner": "ali"}), 200, "put ulang")
	gr = e.must(e.do(adminAK, adminSK, "GET", "/docs/folder/a%20b.txt", nil, nil), 200, "get ulang")
	if string(gr.body) != "baru" {
		t.Fatalf("get ulang: %s", gr.body)
	}

	// copy
	cr := e.must(e.do(adminAK, adminSK, "PUT", "/docs/salinan.txt", nil, map[string]string{"x-amz-copy-source": "/docs/folder/a%20b.txt"}), 200, "copy")
	if !strings.Contains(string(cr.body), "CopyObjectResult") {
		t.Fatalf("copy: %s", cr.body)
	}
	gr = e.must(e.do(adminAK, adminSK, "GET", "/docs/salinan.txt", nil, nil), 200, "get salinan")
	if string(gr.body) != "baru" || gr.hdr.Get("X-Amz-Meta-Owner") != "ali" {
		t.Fatalf("salinan: %s %v", gr.body, gr.hdr)
	}

	// bucket tidak kosong
	e.must(e.do(adminAK, adminSK, "DELETE", "/docs", nil, nil), 409, "hapus bucket isi")
	// delete objects
	del := `<Delete><Object><Key>folder/a b.txt</Key></Object><Object><Key>salinan.txt</Key></Object><Object><Key>tidak-ada</Key></Object></Delete>`
	dr := e.must(e.do(adminAK, adminSK, "POST", "/docs?delete", []byte(del), nil), 200, "delete objects")
	if strings.Count(string(dr.body), "<Deleted>") != 3 {
		t.Fatalf("delete objects: %s", dr.body)
	}
	e.must(e.do(adminAK, adminSK, "DELETE", "/docs", nil, nil), 204, "hapus bucket")
	e.must(e.do(adminAK, adminSK, "HEAD", "/docs", nil, nil), 404, "bucket terhapus")
}

type listResult struct {
	IsTruncated           bool     `xml:"IsTruncated"`
	NextContinuationToken string   `xml:"NextContinuationToken"`
	NextMarker            string   `xml:"NextMarker"`
	KeyCount              int      `xml:"KeyCount"`
	Keys                  []string `xml:"Contents>Key"`
	Prefixes              []string `xml:"CommonPrefixes>Prefix"`
}

func TestListObjects(t *testing.T) {
	e := newEnv(t)
	e.must(e.do(adminAK, adminSK, "PUT", "/lst", nil, nil), 200, "buat bucket")
	keys := []string{"a.txt", "b.txt", "dir1/x.txt", "dir1/y.txt", "dir1/sub/z.txt", "dir2/p.txt", "c.txt", "dir1/"}
	for _, k := range keys {
		e.must(e.do(adminAK, adminSK, "PUT", "/lst/"+k, []byte(k), nil), 200, "put "+k)
	}
	parse := func(r resp) listResult {
		var lr listResult
		if err := xml.Unmarshal(r.body, &lr); err != nil {
			t.Fatalf("xml: %v %s", err, r.body)
		}
		return lr
	}
	// delimiter, semua
	lr := parse(e.must(e.do(adminAK, adminSK, "GET", "/lst?list-type=2&delimiter=/", nil, nil), 200, "list root"))
	if strings.Join(lr.Keys, ",") != "a.txt,b.txt,c.txt" || strings.Join(lr.Prefixes, ",") != "dir1/,dir2/" || lr.KeyCount != 5 {
		t.Fatalf("list root: %+v", lr)
	}
	// prefix di dalam folder
	lr = parse(e.must(e.do(adminAK, adminSK, "GET", "/lst?list-type=2&delimiter=/&prefix=dir1/", nil, nil), 200, "list dir1"))
	if strings.Join(lr.Keys, ",") != "dir1/,dir1/x.txt,dir1/y.txt" || strings.Join(lr.Prefixes, ",") != "dir1/sub/" {
		t.Fatalf("list dir1: %+v", lr)
	}
	// paginasi max-keys=2 dengan delimiter
	var all []string
	tok := ""
	for i := 0; i < 10; i++ {
		u := "/lst?list-type=2&delimiter=/&max-keys=2"
		if tok != "" {
			u += "&continuation-token=" + url.QueryEscape(tok)
		}
		lr = parse(e.must(e.do(adminAK, adminSK, "GET", u, nil, nil), 200, "page"))
		all = append(all, lr.Keys...)
		all = append(all, lr.Prefixes...)
		if !lr.IsTruncated {
			break
		}
		tok = lr.NextContinuationToken
	}
	if strings.Join(all, ",") != "a.txt,b.txt,c.txt,dir1/,dir2/" {
		t.Fatalf("paginasi: %v", all)
	}
	// tanpa delimiter, rekursif, v1 marker
	all = nil
	marker := ""
	for i := 0; i < 10; i++ {
		u := "/lst?max-keys=3"
		if marker != "" {
			u += "&marker=" + url.QueryEscape(marker)
		}
		lr = parse(e.must(e.do(adminAK, adminSK, "GET", u, nil, nil), 200, "v1"))
		all = append(all, lr.Keys...)
		if !lr.IsTruncated {
			break
		}
		marker = lr.NextMarker
	}
	if len(all) != 8 {
		t.Fatalf("v1 rekursif: %v", all)
	}
	// encoding-type=url
	e.must(e.do(adminAK, adminSK, "PUT", "/lst/sp%20ace%2Bplus.txt", []byte("x"), nil), 200, "put spasi")
	lr = parse(e.must(e.do(adminAK, adminSK, "GET", "/lst?list-type=2&prefix=sp&encoding-type=url", nil, nil), 200, "enc"))
	if len(lr.Keys) != 1 || lr.Keys[0] != "sp+ace%2Bplus.txt" {
		t.Fatalf("encoding url: %+v", lr)
	}
}

func TestMultipart(t *testing.T) {
	e := newEnv(t)
	e.must(e.do(adminAK, adminSK, "PUT", "/mpart", nil, nil), 200, "buat bucket")
	ir := e.must(e.do(adminAK, adminSK, "POST", "/mpart/besar.bin?uploads", nil, map[string]string{"Content-Type": "application/x-test"}), 200, "initiate")
	var init struct {
		UploadID string `xml:"UploadId"`
	}
	xml.Unmarshal(ir.body, &init)
	if init.UploadID == "" {
		t.Fatalf("initiate: %s", ir.body)
	}
	p1 := bytes.Repeat([]byte("1"), 5*1024*1024)
	p2 := []byte("ekor")
	r1 := e.must(e.do(adminAK, adminSK, "PUT", "/mpart/besar.bin?partNumber=1&uploadId="+init.UploadID, p1, nil), 200, "part1")
	r2 := e.must(e.do(adminAK, adminSK, "PUT", "/mpart/besar.bin?partNumber=2&uploadId="+init.UploadID, p2, nil), 200, "part2")
	lp := e.must(e.do(adminAK, adminSK, "GET", "/mpart/besar.bin?uploadId="+init.UploadID, nil, nil), 200, "list parts")
	if strings.Count(string(lp.body), "<Part>") != 2 {
		t.Fatalf("list parts: %s", lp.body)
	}
	lu := e.must(e.do(adminAK, adminSK, "GET", "/mpart?uploads", nil, nil), 200, "list uploads")
	if !strings.Contains(string(lu.body), init.UploadID) {
		t.Fatalf("list uploads: %s", lu.body)
	}
	// urutan salah
	bad := fmt.Sprintf(`<CompleteMultipartUpload><Part><PartNumber>2</PartNumber><ETag>%s</ETag></Part><Part><PartNumber>1</PartNumber><ETag>%s</ETag></Part></CompleteMultipartUpload>`, r2.hdr.Get("ETag"), r1.hdr.Get("ETag"))
	e.must(e.do(adminAK, adminSK, "POST", "/mpart/besar.bin?uploadId="+init.UploadID, []byte(bad), nil), 400, "urutan salah")
	good := fmt.Sprintf(`<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>%s</ETag></Part><Part><PartNumber>2</PartNumber><ETag>%s</ETag></Part></CompleteMultipartUpload>`, r1.hdr.Get("ETag"), r2.hdr.Get("ETag"))
	cr := e.must(e.do(adminAK, adminSK, "POST", "/mpart/besar.bin?uploadId="+init.UploadID, []byte(good), nil), 200, "complete")
	var done struct {
		ETag string `xml:"ETag"`
	}
	xml.Unmarshal(cr.body, &done)
	if !strings.HasSuffix(done.ETag, `-2"`) {
		t.Fatalf("complete: %s", cr.body)
	}
	gr := e.must(e.do(adminAK, adminSK, "GET", "/mpart/besar.bin", nil, nil), 200, "get gabungan")
	if len(gr.body) != len(p1)+len(p2) || !bytes.HasSuffix(gr.body, p2) || gr.hdr.Get("Content-Type") != "application/x-test" {
		t.Fatalf("gabungan: len=%d ct=%s", len(gr.body), gr.hdr.Get("Content-Type"))
	}
	e.must(e.do(adminAK, adminSK, "GET", "/mpart/besar.bin?uploadId="+init.UploadID, nil, nil), 404, "upload sudah selesai")

	// abort
	ir = e.must(e.do(adminAK, adminSK, "POST", "/mpart/batal.bin?uploads", nil, nil), 200, "initiate 2")
	xml.Unmarshal(ir.body, &init)
	e.must(e.do(adminAK, adminSK, "PUT", "/mpart/batal.bin?partNumber=1&uploadId="+init.UploadID, []byte("x"), nil), 200, "part")
	e.must(e.do(adminAK, adminSK, "DELETE", "/mpart/batal.bin?uploadId="+init.UploadID, nil, nil), 204, "abort")
	e.must(e.do(adminAK, adminSK, "GET", "/mpart/batal.bin", nil, nil), 404, "tidak ada objek setelah abort")
}

func TestPermissionsAndPublic(t *testing.T) {
	e := newEnv(t)
	e.must(e.do(userAK, userSK, "PUT", "/shared", nil, nil), 403, "user buat bucket")
	e.must(e.do(adminAK, adminSK, "PUT", "/shared", nil, nil), 200, "admin buat bucket")
	e.must(e.do(adminAK, adminSK, "PUT", "/private", nil, nil), 200, "admin buat bucket 2")
	e.must(e.do(adminAK, adminSK, "PUT", "/shared/f.txt", []byte("f"), nil), 200, "admin put")
	e.must(e.do(userAK, userSK, "GET", "/shared/f.txt", nil, nil), 200, "user baca")
	e.must(e.do(userAK, userSK, "PUT", "/shared/g.txt", []byte("g"), nil), 403, "user tulis")
	e.must(e.do(userAK, userSK, "GET", "/private", nil, nil), 403, "user list bucket lain")
	lb := e.must(e.do(userAK, userSK, "GET", "/", nil, nil), 200, "user list buckets")
	if strings.Contains(string(lb.body), "private") {
		t.Fatalf("user melihat bucket lain: %s", lb.body)
	}
	e.must(e.do("WRONG", "x", "GET", "/shared/f.txt", nil, nil), 403, "key salah")
	e.must(e.do(userAK, "salah", "GET", "/shared/f.txt", nil, nil), 403, "secret salah")

	// public
	e.must(e.do("", "", "GET", "/shared/f.txt", nil, nil), 403, "anonim sebelum public")
	e.s.Meta.SetBucketPublic(context.Background(), "shared", true)
	e.must(e.do("", "", "GET", "/shared/f.txt", nil, nil), 200, "anonim setelah public")
	e.must(e.do("", "", "GET", "/shared", nil, nil), 403, "anonim list tetap ditolak")
	e.must(e.do("", "", "PUT", "/shared/h.txt", []byte("h"), nil), 403, "anonim tulis")

	// presigned
	req, _ := http.NewRequest("GET", e.srv.URL+"/private/rahasia.txt?response-content-disposition="+url.QueryEscape(`attachment; filename="r.txt"`), nil)
	e.must(e.do(adminAK, adminSK, "PUT", "/private/rahasia.txt", []byte("r"), nil), 200, "put rahasia")
	u := sigv4.Presign(req, adminAK, adminSK, "us-east-1", time.Now(), time.Minute)
	res, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || string(b) != "r" || res.Header.Get("Content-Disposition") != `attachment; filename="r.txt"` {
		t.Fatalf("presigned: %d %s %v", res.StatusCode, b, res.Header)
	}
	res, _ = http.Get(strings.Replace(u, "X-Amz-Signature=", "X-Amz-Signature=00", 1))
	if res.StatusCode != 403 {
		t.Fatalf("presigned rusak: %d", res.StatusCode)
	}
}

func TestVirtualHost(t *testing.T) {
	e := newEnv(t)
	e.s.Domain = "s3.local"
	req, _ := http.NewRequest("PUT", e.srv.URL+"/", nil)
	req.Host = "vhost.s3.local"
	sigv4.Sign(req, adminAK, adminSK, "us-east-1", time.Now(), sigv4.EmptySHA256)
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("buat bucket vhost: %v %d", err, res.StatusCode)
	}
	if _, err := e.s.Meta.GetBucket(context.Background(), "vhost"); err != nil {
		t.Fatalf("bucket vhost tidak ada: %v", err)
	}
}
