// Package sigv4 memverifikasi (dan untuk tes: membuat) tanda tangan AWS Signature Version 4
// sebagaimana dipakai klien S3 (header Authorization maupun presigned URL).
package sigv4

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	Algorithm = "AWS4-HMAC-SHA256"

	UnsignedPayload          = "UNSIGNED-PAYLOAD"
	StreamingSignedPayload   = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"
	StreamingSignedTrailer   = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER"
	StreamingUnsignedTrailer = "STREAMING-UNSIGNED-PAYLOAD-TRAILER"

	amzDateFormat = "20060102T150405Z"
	MaxSkew       = 15 * time.Minute
	EmptySHA256   = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// Lookup mengembalikan secret key untuk access key; ok=false bila tidak dikenal.
type Lookup func(accessKey string) (secret string, ok bool)

// Error adalah kesalahan autentikasi dengan kode S3.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func errf(code, msg string) *Error { return &Error{Code: code, Message: msg} }

// Result berisi hasil verifikasi yang dibutuhkan handler (mis. untuk aws-chunked).
type Result struct {
	AccessKey     string
	Region        string
	Service       string
	Date          time.Time // x-amz-date
	DateStamp     string    // YYYYMMDD
	Scope         string    // date/region/service/aws4_request
	Signature     string    // hex; seed signature untuk chunked
	SigningKey    []byte
	ContentSHA256 string // nilai x-amz-content-sha256 (atau UNSIGNED-PAYLOAD untuk presigned)
	Presigned     bool
}

// IsSigned melaporkan apakah request membawa kredensial SigV4 (header atau query).
func IsSigned(r *http.Request) bool {
	if strings.HasPrefix(r.Header.Get("Authorization"), Algorithm) {
		return true
	}
	return r.URL.Query().Get("X-Amz-Algorithm") == Algorithm
}

// Verify memverifikasi request. Mengembalikan *Error bila gagal.
func Verify(r *http.Request, lookup Lookup, now time.Time) (*Result, error) {
	if strings.HasPrefix(r.Header.Get("Authorization"), Algorithm) {
		return verifyHeader(r, lookup, now)
	}
	if r.URL.Query().Get("X-Amz-Algorithm") == Algorithm {
		return verifyPresigned(r, lookup, now)
	}
	return nil, errf("AccessDenied", "Request tidak bertanda tangan")
}

func parseCredential(cred string) (ak, dateStamp, region, service string, err error) {
	parts := strings.Split(cred, "/")
	if len(parts) != 5 || parts[4] != "aws4_request" {
		return "", "", "", "", errf("AuthorizationHeaderMalformed", "Credential harus berformat <access-key>/<date>/<region>/<service>/aws4_request")
	}
	return parts[0], parts[1], parts[2], parts[3], nil
}

func verifyHeader(r *http.Request, lookup Lookup, now time.Time) (*Result, error) {
	auth := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), Algorithm))
	var credential, signedHeaders, signature string
	for _, field := range strings.Split(auth, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(field), "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "Credential":
			credential = strings.TrimSpace(v)
		case "SignedHeaders":
			signedHeaders = strings.TrimSpace(v)
		case "Signature":
			signature = strings.TrimSpace(v)
		}
	}
	if credential == "" || signedHeaders == "" || signature == "" {
		return nil, errf("AuthorizationHeaderMalformed", "Header Authorization tidak lengkap")
	}
	ak, dateStamp, region, service, err := parseCredential(credential)
	if err != nil {
		return nil, err
	}

	amzDate := r.Header.Get("X-Amz-Date")
	if amzDate == "" {
		if d := r.Header.Get("Date"); d != "" {
			if t, err := http.ParseTime(d); err == nil {
				amzDate = t.UTC().Format(amzDateFormat)
			}
		}
	}
	if amzDate == "" {
		return nil, errf("AccessDenied", "Header x-amz-date wajib ada")
	}
	t, err := time.Parse(amzDateFormat, amzDate)
	if err != nil {
		return nil, errf("AccessDenied", "Format x-amz-date tidak valid")
	}
	if d := now.Sub(t); d > MaxSkew || d < -MaxSkew {
		return nil, errf("RequestTimeTooSkewed", "Selisih waktu request dan server terlalu besar")
	}
	if t.Format("20060102") != dateStamp {
		return nil, errf("AuthorizationHeaderMalformed", "Tanggal pada Credential tidak cocok dengan x-amz-date")
	}

	secret, ok := lookup(ak)
	if !ok {
		return nil, errf("InvalidAccessKeyId", "Access key tidak dikenal")
	}

	contentSHA := r.Header.Get("X-Amz-Content-Sha256")
	if contentSHA == "" {
		contentSHA = UnsignedPayload
	}

	headers := strings.Split(signedHeaders, ";")
	canonical := canonicalRequest(r, headers, contentSHA, r.URL.Query())
	scope := dateStamp + "/" + region + "/" + service + "/aws4_request"
	sts := stringToSign(amzDate, scope, canonical)
	key := signingKey(secret, dateStamp, region, service)
	expected := hex.EncodeToString(hmacSHA256(key, sts))
	if subtle.ConstantTimeCompare([]byte(expected), []byte(strings.ToLower(signature))) != 1 {
		return nil, errf("SignatureDoesNotMatch", "Tanda tangan request tidak cocok")
	}
	return &Result{
		AccessKey: ak, Region: region, Service: service, Date: t, DateStamp: dateStamp,
		Scope: scope, Signature: expected, SigningKey: key, ContentSHA256: contentSHA,
	}, nil
}

func verifyPresigned(r *http.Request, lookup Lookup, now time.Time) (*Result, error) {
	q := r.URL.Query()
	credential := q.Get("X-Amz-Credential")
	amzDate := q.Get("X-Amz-Date")
	expiresStr := q.Get("X-Amz-Expires")
	signedHeaders := q.Get("X-Amz-SignedHeaders")
	signature := q.Get("X-Amz-Signature")
	if credential == "" || amzDate == "" || expiresStr == "" || signedHeaders == "" || signature == "" {
		return nil, errf("AuthorizationQueryParametersError", "Parameter presigned tidak lengkap")
	}
	ak, dateStamp, region, service, err := parseCredential(credential)
	if err != nil {
		return nil, err
	}
	t, err := time.Parse(amzDateFormat, amzDate)
	if err != nil {
		return nil, errf("AuthorizationQueryParametersError", "X-Amz-Date tidak valid")
	}
	expires, err := strconv.ParseInt(expiresStr, 10, 64)
	if err != nil || expires < 1 || expires > 7*24*3600 {
		return nil, errf("AuthorizationQueryParametersError", "X-Amz-Expires harus 1..604800")
	}
	if t.After(now.Add(MaxSkew)) {
		return nil, errf("RequestTimeTooSkewed", "X-Amz-Date berada di masa depan")
	}
	if now.After(t.Add(time.Duration(expires) * time.Second)) {
		return nil, errf("AccessDenied", "Presigned URL sudah kedaluwarsa")
	}
	secret, ok := lookup(ak)
	if !ok {
		return nil, errf("InvalidAccessKeyId", "Access key tidak dikenal")
	}

	q.Del("X-Amz-Signature")
	headers := strings.Split(signedHeaders, ";")
	contentSHA := q.Get("X-Amz-Content-Sha256")
	if contentSHA == "" {
		contentSHA = UnsignedPayload
	}
	canonical := canonicalRequest(r, headers, contentSHA, q)
	scope := dateStamp + "/" + region + "/" + service + "/aws4_request"
	sts := stringToSign(amzDate, scope, canonical)
	key := signingKey(secret, dateStamp, region, service)
	expected := hex.EncodeToString(hmacSHA256(key, sts))
	if subtle.ConstantTimeCompare([]byte(expected), []byte(strings.ToLower(signature))) != 1 {
		return nil, errf("SignatureDoesNotMatch", "Tanda tangan presigned URL tidak cocok")
	}
	return &Result{
		AccessKey: ak, Region: region, Service: service, Date: t, DateStamp: dateStamp,
		Scope: scope, Signature: expected, SigningKey: key, ContentSHA256: contentSHA, Presigned: true,
	}, nil
}

// ---------- kanonikalisasi ----------

func canonicalRequest(r *http.Request, signedHeaders []string, payloadHash string, query url.Values) string {
	var b strings.Builder
	b.WriteString(r.Method)
	b.WriteByte('\n')
	b.WriteString(EncodePath(r.URL.Path))
	b.WriteByte('\n')
	b.WriteString(CanonicalQuery(query))
	b.WriteByte('\n')
	for _, h := range signedHeaders {
		h = strings.ToLower(strings.TrimSpace(h))
		b.WriteString(h)
		b.WriteByte(':')
		b.WriteString(headerValue(r, h))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(strings.Join(lowerAll(signedHeaders), ";"))
	b.WriteByte('\n')
	b.WriteString(payloadHash)
	return b.String()
}

func lowerAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToLower(strings.TrimSpace(s))
	}
	return out
}

func headerValue(r *http.Request, name string) string {
	switch name {
	case "host":
		return strings.TrimSpace(r.Host)
	case "content-length":
		if v := r.Header.Get("Content-Length"); v != "" {
			return strings.TrimSpace(v)
		}
		if r.ContentLength >= 0 {
			return strconv.FormatInt(r.ContentLength, 10)
		}
		return ""
	}
	vals := r.Header.Values(name)
	parts := make([]string, 0, len(vals))
	for _, v := range vals {
		parts = append(parts, strings.Join(strings.Fields(v), " "))
	}
	return strings.Join(parts, ",")
}

func stringToSign(amzDate, scope, canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	return Algorithm + "\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func signingKey(secret, dateStamp, region, service string) []byte {
	k := hmacSHA256([]byte("AWS4"+secret), dateStamp)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, service)
	return hmacSHA256(k, "aws4_request")
}

// EncodePath meng-encode path (sudah ter-decode) sesuai aturan AWS: hanya
// unreserved (A-Z a-z 0-9 - _ . ~) dan "/" yang dibiarkan, sisanya %XX huruf besar.
func EncodePath(p string) string {
	if p == "" {
		return "/"
	}
	return awsEncode(p, true)
}

func awsEncode(s string, keepSlash bool) string {
	const hexChars = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && keepSlash:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexChars[c>>4])
			b.WriteByte(hexChars[c&15])
		}
	}
	return b.String()
}

// CanonicalQuery membangun query string kanonik (terurut, ter-encode).
func CanonicalQuery(q url.Values) string {
	type kv struct{ k, v string }
	var pairs []kv
	for k, vals := range q {
		ek := awsEncode(k, false)
		for _, v := range vals {
			pairs = append(pairs, kv{ek, awsEncode(v, false)})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k != pairs[j].k {
			return pairs[i].k < pairs[j].k
		}
		return pairs[i].v < pairs[j].v
	})
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = p.k + "=" + p.v
	}
	return strings.Join(parts, "&")
}

// ---------- penanda tangan (untuk tes & klien internal) ----------

// Sign menambahkan header Authorization SigV4 ke request. payloadHash boleh
// UnsignedPayload. Header host, x-amz-date, x-amz-content-sha256 ikut ditandatangani
// beserta header lain yang disebutkan di extraHeaders (harus sudah ada di request).
func Sign(r *http.Request, accessKey, secret, region string, now time.Time, payloadHash string, extraHeaders ...string) {
	amzDate := now.UTC().Format(amzDateFormat)
	dateStamp := now.UTC().Format("20060102")
	r.Header.Set("X-Amz-Date", amzDate)
	r.Header.Set("X-Amz-Content-Sha256", payloadHash)
	headers := append([]string{"host", "x-amz-content-sha256", "x-amz-date"}, lowerAll(extraHeaders)...)
	sort.Strings(headers)
	canonical := canonicalRequest(r, headers, payloadHash, r.URL.Query())
	scope := dateStamp + "/" + region + "/s3/aws4_request"
	sts := stringToSign(amzDate, scope, canonical)
	sig := hex.EncodeToString(hmacSHA256(signingKey(secret, dateStamp, region, "s3"), sts))
	r.Header.Set("Authorization", Algorithm+" Credential="+accessKey+"/"+scope+", SignedHeaders="+strings.Join(headers, ";")+", Signature="+sig)
}

// Presign menghasilkan URL presigned untuk request (method+URL+host).
func Presign(r *http.Request, accessKey, secret, region string, now time.Time, expires time.Duration) string {
	amzDate := now.UTC().Format(amzDateFormat)
	dateStamp := now.UTC().Format("20060102")
	scope := dateStamp + "/" + region + "/s3/aws4_request"
	q := r.URL.Query()
	q.Set("X-Amz-Algorithm", Algorithm)
	q.Set("X-Amz-Credential", accessKey+"/"+scope)
	q.Set("X-Amz-Date", amzDate)
	q.Set("X-Amz-Expires", strconv.Itoa(int(expires.Seconds())))
	q.Set("X-Amz-SignedHeaders", "host")
	canonical := canonicalRequest(r, []string{"host"}, UnsignedPayload, q)
	sts := stringToSign(amzDate, scope, canonical)
	sig := hex.EncodeToString(hmacSHA256(signingKey(secret, dateStamp, region, "s3"), sts))
	q.Set("X-Amz-Signature", sig)
	u := *r.URL
	u.RawQuery = q.Encode()
	return u.String()
}

var ErrChunkSignature = errors.New("tanda tangan chunk tidak cocok")
