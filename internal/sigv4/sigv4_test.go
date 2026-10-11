package sigv4

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	exAK     = "AKIAIOSFODNN7EXAMPLE"
	exSecret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
)

func lookup(ak string) (string, bool) {
	if ak == exAK {
		return exSecret, true
	}
	return "", false
}

// Vektor resmi dari dokumentasi AWS "Signature Calculations for the Authorization Header: GET Object".
func TestAWSExampleGetObject(t *testing.T) {
	r := httptest.NewRequest("GET", "http://examplebucket.s3.amazonaws.com/test.txt", nil)
	r.Host = "examplebucket.s3.amazonaws.com"
	r.Header.Set("Range", "bytes=0-9")
	r.Header.Set("x-amz-content-sha256", EmptySHA256)
	r.Header.Set("x-amz-date", "20130524T000000Z")
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request,SignedHeaders=host;range;x-amz-content-sha256;x-amz-date,Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41")
	now := time.Date(2013, 5, 24, 0, 0, 10, 0, time.UTC)
	res, err := Verify(r, lookup, now)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.AccessKey != exAK || res.Region != "us-east-1" {
		t.Fatalf("hasil tidak sesuai: %+v", res)
	}
}

// Vektor resmi: GET Bucket Lifecycle (?lifecycle) menguji query kanonik.
func TestAWSExampleQuery(t *testing.T) {
	r := httptest.NewRequest("GET", "http://examplebucket.s3.amazonaws.com/?lifecycle", nil)
	r.Host = "examplebucket.s3.amazonaws.com"
	r.Header.Set("x-amz-content-sha256", EmptySHA256)
	r.Header.Set("x-amz-date", "20130524T000000Z")
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request,SignedHeaders=host;x-amz-content-sha256;x-amz-date,Signature=fea454ca298b7da1c68078a5d1bdbfbbe0d65c699e0f91ac7a200a0136783543")
	now := time.Date(2013, 5, 24, 0, 0, 10, 0, time.UTC)
	if _, err := Verify(r, lookup, now); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

// Vektor resmi: presigned GET dengan expires 86400.
func TestAWSExamplePresigned(t *testing.T) {
	u := "http://examplebucket.s3.amazonaws.com/test.txt?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20130524%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-Date=20130524T000000Z&X-Amz-Expires=86400&X-Amz-SignedHeaders=host&X-Amz-Signature=aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404"
	r := httptest.NewRequest("GET", u, nil)
	r.Host = "examplebucket.s3.amazonaws.com"
	now := time.Date(2013, 5, 24, 1, 0, 0, 0, time.UTC)
	res, err := Verify(r, lookup, now)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !res.Presigned {
		t.Fatal("harus presigned")
	}
	if _, err := Verify(r, lookup, now.Add(48*time.Hour)); err == nil {
		t.Fatal("harus kedaluwarsa")
	}
}

func TestRoundTripSignAndTamper(t *testing.T) {
	body := []byte("halo dunia")
	sum := sha256.Sum256(body)
	r := httptest.NewRequest("PUT", "http://localhost:9000/bucket/folder/file%20name%2Bx.txt?partNumber=1&uploadId=abc", bytes.NewReader(body))
	r.Host = "localhost:9000"
	r.Header.Set("Content-Type", "text/plain")
	now := time.Now()
	Sign(r, exAK, exSecret, "ap-southeast-1", now, hex.EncodeToString(sum[:]), "content-type")
	if _, err := Verify(r, lookup, now.Add(time.Minute)); err != nil {
		t.Fatalf("verify: %v", err)
	}
	r.Header.Set("Content-Type", "text/html")
	if _, err := Verify(r, lookup, now); err == nil {
		t.Fatal("perubahan header bertanda tangan harus gagal")
	}
	r.Header.Set("Content-Type", "text/plain")
	if _, err := Verify(r, lookup, now.Add(20*time.Minute)); err == nil {
		t.Fatal("skew 20 menit harus ditolak")
	}
	if _, err := Verify(r, func(string) (string, bool) { return "lain", true }, now); err == nil {
		t.Fatal("secret salah harus gagal")
	}
}

func TestPresignRoundTrip(t *testing.T) {
	r := httptest.NewRequest("GET", "http://localhost:9000/b/k%20with%20space.pdf?response-content-disposition=attachment%3B%20filename%3D%22a.pdf%22", nil)
	r.Host = "localhost:9000"
	now := time.Now()
	u := Presign(r, exAK, exSecret, "us-east-1", now, 5*time.Minute)
	r2 := httptest.NewRequest("GET", u, nil)
	r2.Host = "localhost:9000"
	if _, err := Verify(r2, lookup, now.Add(time.Minute)); err != nil {
		t.Fatalf("verify presigned: %v", err)
	}
	if _, err := Verify(r2, lookup, now.Add(10*time.Minute)); err == nil {
		t.Fatal("harus kedaluwarsa")
	}
}

// Vektor resmi: PUT aws-chunked 66560 byte (3 chunk: 65536, 1024, 0).
func TestChunkedSigned(t *testing.T) {
	seed := "4f232c4386841ef735655705268965c44a0e4690baa4adea153f7db9fa80a0a9"
	res := &Result{
		Date: time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC), Scope: "20130524/us-east-1/s3/aws4_request",
		Signature: seed, SigningKey: signingKey(exSecret, "20130524", "us-east-1", "s3"), ContentSHA256: StreamingSignedPayload,
	}
	a := bytes.Repeat([]byte("a"), 65536)
	b := bytes.Repeat([]byte("a"), 1024)
	var buf bytes.Buffer
	buf.WriteString("10000;chunk-signature=ad80c730a21e5b8d04586a2213dd63b9a0e99e0e2307b0ade35a65485a288648\r\n")
	buf.Write(a)
	buf.WriteString("\r\n400;chunk-signature=0055627c9e194cb4542bae2aa5492e3c1575bbb81b612b7d234b86a503ef5497\r\n")
	buf.Write(b)
	buf.WriteString("\r\n0;chunk-signature=b6c6ea8a5354eaf15b3cb7646744f4275b71ea724fed81ceb9323e279d449df9\r\n\r\n")
	out, err := io.ReadAll(NewChunkedReader(&buf, res))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(out) != 66560 {
		t.Fatalf("panjang %d", len(out))
	}
	// chunk dimanipulasi
	buf.Reset()
	buf.WriteString("10000;chunk-signature=ad80c730a21e5b8d04586a2213dd63b9a0e99e0e2307b0ade35a65485a288648\r\n")
	a[0] = 'b'
	buf.Write(a)
	buf.WriteString("\r\n0;chunk-signature=b6c6ea8a5354eaf15b3cb7646744f4275b71ea724fed81ceb9323e279d449df9\r\n\r\n")
	if _, err := io.ReadAll(NewChunkedReader(&buf, res)); err == nil {
		t.Fatal("chunk yang diubah harus ditolak")
	}
}

func TestChunkedUnsignedTrailer(t *testing.T) {
	res := &Result{ContentSHA256: StreamingUnsignedTrailer}
	raw := "5\r\nhello\r\n6\r\n world\r\n0\r\nx-amz-checksum-crc32:abcd\r\n\r\n"
	out, err := io.ReadAll(NewChunkedReader(strings.NewReader(raw), res))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(out) != "hello world" {
		t.Fatalf("got %q", out)
	}
}

func TestEncodePath(t *testing.T) {
	cases := map[string]string{
		"/a b/c+d":   "/a%20b/c%2Bd",
		"/":          "/",
		"/x~y_z.-":   "/x~y_z.-",
		"/ünï/코드":    "/%C3%BCn%C3%AF/%EC%BD%94%EB%93%9C",
		"/dir/":      "/dir/",
		"/a%b":       "/a%25b",
		"/@!$&'()*,": "/%40%21%24%26%27%28%29%2A%2C",
	}
	for in, want := range cases {
		if got := EncodePath(in); got != want {
			t.Errorf("EncodePath(%q)=%q want %q", in, got, want)
		}
	}
}

var _ = http.MethodGet
