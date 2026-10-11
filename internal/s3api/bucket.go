package s3api

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/alisyarudin/jasnita-storage/internal/meta"
)

type ownerXML struct {
	ID          string `xml:"ID"`
	DisplayName string `xml:"DisplayName"`
}

type listBucketsXML struct {
	XMLName xml.Name `xml:"ListAllMyBucketsResult"`
	NS      string   `xml:"xmlns,attr"`
	Owner   ownerXML `xml:"Owner"`
	Buckets []struct {
		Name         string `xml:"Name"`
		CreationDate string `xml:"CreationDate"`
	} `xml:"Buckets>Bucket"`
}

func (s *Server) listBuckets(ctx context.Context, w http.ResponseWriter, info *reqInfo) error {
	if info.key == nil {
		return errAccessDenied
	}
	all, err := s.Meta.ListBuckets(ctx)
	if err != nil {
		return err
	}
	out := listBucketsXML{NS: xmlNS, Owner: ownerXML{ID: info.key.AccessKey, DisplayName: info.key.Name}}
	for _, b := range all {
		if !info.key.CanRead(b.Name) {
			continue
		}
		out.Buckets = append(out.Buckets, struct {
			Name         string `xml:"Name"`
			CreationDate string `xml:"CreationDate"`
		}{b.Name, b.CreatedAt.UTC().Format(iso8601)})
	}
	s.writeXMLOK(w, out)
	return nil
}

func (s *Server) createBucket(ctx context.Context, w http.ResponseWriter, r *http.Request, info *reqInfo) error {
	if info.key == nil {
		return errAccessDenied
	}
	if !ValidBucketName(info.bucket) {
		return newErr("InvalidBucketName", "Nama bucket tidak valid (3-63 huruf kecil, angka, titik, strip)")
	}
	if !info.key.CanCreateBucket() {
		return errAccessDenied
	}
	err := s.Meta.CreateBucket(ctx, meta.Bucket{Name: info.bucket, Owner: info.key.AccessKey, CreatedAt: time.Now()})
	if errors.Is(err, meta.ErrExists) {
		return newErr("BucketAlreadyOwnedByYou", "Bucket dengan nama ini sudah ada")
	}
	if err != nil {
		return err
	}
	if err := s.Blob.EnsureBucket(info.bucket); err != nil {
		return err
	}
	w.Header().Set("Location", "/"+info.bucket)
	w.WriteHeader(http.StatusOK)
	return nil
}

func (s *Server) headBucket(ctx context.Context, w http.ResponseWriter, info *reqInfo) error {
	if _, err := s.requireRead(ctx, info, true); err != nil {
		return err
	}
	w.Header().Set("X-Amz-Bucket-Region", s.Region)
	w.WriteHeader(http.StatusOK)
	return nil
}

func (s *Server) deleteBucket(ctx context.Context, w http.ResponseWriter, info *reqInfo) error {
	if _, err := s.requireWrite(ctx, info); err != nil {
		return err
	}
	err := s.Meta.DeleteBucket(ctx, info.bucket)
	if errors.Is(err, meta.ErrNotEmpty) {
		return newErr("BucketNotEmpty", "Bucket masih berisi objek atau upload multipart")
	}
	if err != nil {
		return err
	}
	if err := s.Blob.RemoveBucket(info.bucket); err != nil {
		s.Log.Warn("hapus direktori bucket", "bucket", info.bucket, "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// DeleteBucketForce menghapus bucket beserta seluruh isinya (dipakai console).
func (s *Server) DeleteBucketForce(ctx context.Context, name string) error {
	if _, err := s.loadBucket(ctx, name); err != nil {
		return err
	}
	if err := s.Meta.PurgeBucket(ctx, name); err != nil {
		return err
	}
	return s.Blob.RemoveBucket(name)
}

type locationXML struct {
	XMLName xml.Name `xml:"LocationConstraint"`
	NS      string   `xml:"xmlns,attr"`
	Region  string   `xml:",chardata"`
}

func (s *Server) getBucketLocation(ctx context.Context, w http.ResponseWriter, info *reqInfo) error {
	if _, err := s.requireRead(ctx, info, true); err != nil {
		return err
	}
	s.writeXMLOK(w, locationXML{NS: xmlNS, Region: s.Region})
	return nil
}

type versioningXML struct {
	XMLName xml.Name `xml:"VersioningConfiguration"`
	NS      string   `xml:"xmlns,attr"`
}

func (s *Server) getBucketVersioning(ctx context.Context, w http.ResponseWriter, info *reqInfo) error {
	if _, err := s.requireRead(ctx, info, false); err != nil {
		return err
	}
	s.writeXMLOK(w, versioningXML{NS: xmlNS})
	return nil
}

type aclXML struct {
	XMLName xml.Name `xml:"AccessControlPolicy"`
	NS      string   `xml:"xmlns,attr"`
	Owner   ownerXML `xml:"Owner"`
	Grants  []struct {
		Grantee struct {
			XSI         string `xml:"xmlns:xsi,attr"`
			Type        string `xml:"xsi:type,attr"`
			ID          string `xml:"ID,omitempty"`
			DisplayName string `xml:"DisplayName,omitempty"`
			URI         string `xml:"URI,omitempty"`
		} `xml:"Grantee"`
		Permission string `xml:"Permission"`
	} `xml:"AccessControlList>Grant"`
}

func (s *Server) getBucketACL(ctx context.Context, w http.ResponseWriter, info *reqInfo) error {
	b, err := s.requireRead(ctx, info, false)
	if err != nil {
		return err
	}
	s.writeXMLOK(w, buildACL(b))
	return nil
}

// getObjectACL: ACL objek mengikuti bucket (public => AllUsers READ). Dipakai Flysystem visibility()/copy().
func (s *Server) getObjectACL(ctx context.Context, w http.ResponseWriter, info *reqInfo) error {
	b, err := s.requireRead(ctx, info, false)
	if err != nil {
		return err
	}
	if _, err := s.Meta.GetObject(ctx, info.bucket, info.object); err != nil {
		return errNoSuchKey
	}
	s.writeXMLOK(w, buildACL(b))
	return nil
}

func buildACL(b *meta.Bucket) aclXML {
	out := aclXML{NS: xmlNS, Owner: ownerXML{ID: b.Owner, DisplayName: b.Owner}}
	var g struct {
		Grantee struct {
			XSI         string `xml:"xmlns:xsi,attr"`
			Type        string `xml:"xsi:type,attr"`
			ID          string `xml:"ID,omitempty"`
			DisplayName string `xml:"DisplayName,omitempty"`
			URI         string `xml:"URI,omitempty"`
		} `xml:"Grantee"`
		Permission string `xml:"Permission"`
	}
	g.Grantee.XSI = "http://www.w3.org/2001/XMLSchema-instance"
	g.Grantee.Type = "CanonicalUser"
	g.Grantee.ID = b.Owner
	g.Grantee.DisplayName = b.Owner
	g.Permission = "FULL_CONTROL"
	out.Grants = append(out.Grants, g)
	if b.Public {
		g.Grantee.Type = "Group"
		g.Grantee.ID, g.Grantee.DisplayName = "", ""
		g.Grantee.URI = "http://acs.amazonaws.com/groups/global/AllUsers"
		g.Permission = "READ"
		out.Grants = append(out.Grants, g)
	}
	return out
}

// ---------- listing objek ----------

type listEntry struct {
	obj    *meta.Object
	prefix string
}

// incr mengembalikan string terkecil yang lebih besar dari semua string berawalan p.
func incr(p string) string {
	b := []byte(p)
	for len(b) > 0 && b[len(b)-1] == 0xff {
		b = b[:len(b)-1]
	}
	if len(b) == 0 {
		return "\xff\xff\xff\xff"
	}
	b[len(b)-1]++
	return string(b)
}

func cursorAfter(e listEntry) string {
	if e.prefix != "" {
		return incr(e.prefix)
	}
	return e.obj.Key + "\x00"
}

// ListEntries melakukan listing dengan prefix/delimiter, dimulai dari cursor (inklusif).
func (s *Server) ListEntries(ctx context.Context, bucket, prefix, delimiter, cursor string, maxKeys int) (entries []listEntry, truncated bool, next string, err error) {
	if maxKeys <= 0 {
		return nil, false, "", nil
	}
	const page = 1000
	for {
		rows, err := s.Meta.ListPage(ctx, bucket, prefix, cursor, page)
		if err != nil {
			return nil, false, "", err
		}
		if len(rows) == 0 {
			break
		}
		skipping := ""
		stop := false
		for i := range rows {
			o := &rows[i]
			if skipping != "" && strings.HasPrefix(o.Key, skipping) {
				continue
			}
			skipping = ""
			if delimiter != "" {
				rest := o.Key[len(prefix):]
				if idx := strings.Index(rest, delimiter); idx >= 0 {
					cp := prefix + rest[:idx+len(delimiter)]
					entries = append(entries, listEntry{prefix: cp})
					skipping = cp
					if len(entries) > maxKeys {
						stop = true
						break
					}
					continue
				}
			}
			entries = append(entries, listEntry{obj: o})
			if len(entries) > maxKeys {
				stop = true
				break
			}
		}
		if stop || len(rows) < page {
			break
		}
		if skipping != "" {
			cursor = incr(skipping)
		} else {
			cursor = rows[len(rows)-1].Key + "\x00"
		}
	}
	if len(entries) > maxKeys {
		entries = entries[:maxKeys]
		return entries, true, cursorAfter(entries[maxKeys-1]), nil
	}
	return entries, false, "", nil
}

func encodeToken(cursor string) string { return base64.URLEncoding.EncodeToString([]byte(cursor)) }
func decodeToken(tok string) (string, bool) {
	b, err := base64.URLEncoding.DecodeString(tok)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// urlEncodeKey mengikuti encoding S3 untuk encoding-type=url (form encoding, "/" dibiarkan).
func urlEncodeKey(k string) string {
	return strings.ReplaceAll(url.QueryEscape(k), "%2F", "/")
}

type contentsXML struct {
	Key          string    `xml:"Key"`
	LastModified string    `xml:"LastModified"`
	ETag         string    `xml:"ETag"`
	Size         int64     `xml:"Size"`
	StorageClass string    `xml:"StorageClass"`
	Owner        *ownerXML `xml:"Owner,omitempty"`
}

type commonPrefixXML struct {
	Prefix string `xml:"Prefix"`
}

type listV2XML struct {
	XMLName               xml.Name          `xml:"ListBucketResult"`
	NS                    string            `xml:"xmlns,attr"`
	Name                  string            `xml:"Name"`
	Prefix                string            `xml:"Prefix"`
	Delimiter             string            `xml:"Delimiter,omitempty"`
	EncodingType          string            `xml:"EncodingType,omitempty"`
	StartAfter            string            `xml:"StartAfter,omitempty"`
	ContinuationToken     string            `xml:"ContinuationToken,omitempty"`
	NextContinuationToken string            `xml:"NextContinuationToken,omitempty"`
	KeyCount              int               `xml:"KeyCount"`
	MaxKeys               int               `xml:"MaxKeys"`
	IsTruncated           bool              `xml:"IsTruncated"`
	Contents              []contentsXML     `xml:"Contents"`
	CommonPrefixes        []commonPrefixXML `xml:"CommonPrefixes"`
}

type listV1XML struct {
	XMLName        xml.Name          `xml:"ListBucketResult"`
	NS             string            `xml:"xmlns,attr"`
	Name           string            `xml:"Name"`
	Prefix         string            `xml:"Prefix"`
	Marker         string            `xml:"Marker"`
	NextMarker     string            `xml:"NextMarker,omitempty"`
	Delimiter      string            `xml:"Delimiter,omitempty"`
	EncodingType   string            `xml:"EncodingType,omitempty"`
	MaxKeys        int               `xml:"MaxKeys"`
	IsTruncated    bool              `xml:"IsTruncated"`
	Contents       []contentsXML     `xml:"Contents"`
	CommonPrefixes []commonPrefixXML `xml:"CommonPrefixes"`
}

func (s *Server) listObjects(ctx context.Context, w http.ResponseWriter, r *http.Request, info *reqInfo) error {
	if _, err := s.requireRead(ctx, info, false); err != nil {
		return err
	}
	q := r.URL.Query()
	prefix := q.Get("prefix")
	delimiter := q.Get("delimiter")
	encoding := q.Get("encoding-type")
	if encoding != "" && encoding != "url" {
		return newErr("InvalidArgument", "encoding-type hanya mendukung url")
	}
	maxKeys := 1000
	if v := q.Get("max-keys"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return newErr("InvalidArgument", "max-keys tidak valid")
		}
		if n < maxKeys {
			maxKeys = n
		}
	}
	enc := func(v string) string {
		if encoding == "url" {
			return urlEncodeKey(v)
		}
		return v
	}

	v2 := q.Get("list-type") == "2"
	cursor := ""
	if v2 {
		if tok := q.Get("continuation-token"); tok != "" {
			c, ok := decodeToken(tok)
			if !ok {
				return newErr("InvalidArgument", "continuation-token tidak valid")
			}
			cursor = c
		} else if sa := q.Get("start-after"); sa != "" {
			cursor = sa + "\x00"
		}
	} else if marker := q.Get("marker"); marker != "" {
		if delimiter != "" && strings.HasSuffix(marker, delimiter) {
			cursor = incr(marker)
		} else {
			cursor = marker + "\x00"
		}
	}

	entries, truncated, next, err := s.ListEntries(ctx, info.bucket, prefix, delimiter, cursor, maxKeys)
	if err != nil {
		return err
	}
	var contents []contentsXML
	var prefixes []commonPrefixXML
	lastEntry := ""
	for _, e := range entries {
		if e.prefix != "" {
			prefixes = append(prefixes, commonPrefixXML{Prefix: enc(e.prefix)})
			lastEntry = e.prefix
			continue
		}
		contents = append(contents, contentsXML{
			Key: enc(e.obj.Key), LastModified: e.obj.ModTime.UTC().Format(iso8601),
			ETag: `"` + e.obj.ETag + `"`, Size: e.obj.Size, StorageClass: "STANDARD",
		})
		lastEntry = e.obj.Key
	}

	if v2 {
		out := listV2XML{
			NS: xmlNS, Name: info.bucket, Prefix: enc(prefix), Delimiter: enc(delimiter), EncodingType: encoding,
			StartAfter: enc(q.Get("start-after")), ContinuationToken: q.Get("continuation-token"),
			KeyCount: len(entries), MaxKeys: maxKeys, IsTruncated: truncated, Contents: contents, CommonPrefixes: prefixes,
		}
		if truncated {
			out.NextContinuationToken = encodeToken(next)
		}
		s.writeXMLOK(w, out)
		return nil
	}
	out := listV1XML{
		NS: xmlNS, Name: info.bucket, Prefix: enc(prefix), Marker: enc(q.Get("marker")), Delimiter: enc(delimiter),
		EncodingType: encoding, MaxKeys: maxKeys, IsTruncated: truncated, Contents: contents, CommonPrefixes: prefixes,
	}
	if truncated {
		out.NextMarker = enc(lastEntry)
	}
	s.writeXMLOK(w, out)
	return nil
}
