// Package meta menyimpan metadata bucket, objek, access key dan multipart upload di SQLite.
package meta

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound = errors.New("tidak ditemukan")
	ErrExists   = errors.New("sudah ada")
	ErrNotEmpty = errors.New("bucket belum kosong")
)

type Bucket struct {
	Name      string    `json:"name"`
	Owner     string    `json:"owner"`
	Public    bool      `json:"public"`
	CreatedAt time.Time `json:"createdAt"`
}

type Object struct {
	Bucket      string            `json:"bucket"`
	Key         string            `json:"key"`
	Size        int64             `json:"size"`
	ETag        string            `json:"etag"` // hex md5 tanpa tanda kutip, atau "<md5>-<n>" untuk multipart
	ContentType string            `json:"contentType"`
	Metadata    map[string]string `json:"metadata,omitempty"` // x-amz-meta-* dan header standar (cache-control, dll)
	Blob        string            `json:"-"`
	ModTime     time.Time         `json:"modified"`
}

// Policy memberi izin satu access key pada satu bucket ("*" = semua bucket).
type Policy struct {
	Bucket     string `json:"bucket"`
	Permission string `json:"permission"` // read | readwrite
}

type AccessKey struct {
	AccessKey string    `json:"accessKey"`
	SecretKey string    `json:"-"`
	Name      string    `json:"name"`
	Admin     bool      `json:"admin"`
	Policies  []Policy  `json:"policies"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"createdAt"`
}

// CanRead/CanWrite memeriksa izin pada bucket tertentu.
func (k *AccessKey) CanRead(bucket string) bool  { return k.can(bucket, false) }
func (k *AccessKey) CanWrite(bucket string) bool { return k.can(bucket, true) }

func (k *AccessKey) can(bucket string, write bool) bool {
	if k == nil || !k.Enabled {
		return false
	}
	if k.Admin {
		return true
	}
	for _, p := range k.Policies {
		if p.Bucket != "*" && p.Bucket != bucket {
			continue
		}
		if p.Permission == "readwrite" || (!write && p.Permission == "read") {
			return true
		}
	}
	return false
}

// CanCreateBucket: admin atau punya readwrite pada "*".
func (k *AccessKey) CanCreateBucket() bool {
	if k == nil || !k.Enabled {
		return false
	}
	if k.Admin {
		return true
	}
	for _, p := range k.Policies {
		if p.Bucket == "*" && p.Permission == "readwrite" {
			return true
		}
	}
	return false
}

type Upload struct {
	ID          string
	Bucket      string
	Key         string
	ContentType string
	Metadata    map[string]string
	CreatedAt   time.Time
}

type Part struct {
	UploadID string
	Number   int
	Size     int64
	ETag     string
	Blob     string
	ModTime  time.Time
}

type DB struct {
	db *sql.DB
}

func Open(path string) (*DB, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite: satu koneksi, serialisasi penulisan tanpa SQLITE_BUSY
	m := &DB{db: db}
	if err := m.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return m, nil
}

func (m *DB) Close() error { return m.db.Close() }

func (m *DB) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS buckets(
			name TEXT PRIMARY KEY,
			owner TEXT NOT NULL,
			public INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS objects(
			bucket TEXT NOT NULL,
			key TEXT NOT NULL,
			size INTEGER NOT NULL,
			etag TEXT NOT NULL,
			content_type TEXT NOT NULL,
			metadata TEXT NOT NULL DEFAULT '{}',
			blob TEXT NOT NULL,
			mtime INTEGER NOT NULL,
			PRIMARY KEY(bucket, key)) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS access_keys(
			access_key TEXT PRIMARY KEY,
			secret_key TEXT NOT NULL,
			name TEXT NOT NULL DEFAULT '',
			admin INTEGER NOT NULL DEFAULT 0,
			policies TEXT NOT NULL DEFAULT '[]',
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS uploads(
			id TEXT PRIMARY KEY,
			bucket TEXT NOT NULL,
			key TEXT NOT NULL,
			content_type TEXT NOT NULL,
			metadata TEXT NOT NULL DEFAULT '{}',
			created_at INTEGER NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS uploads_bucket ON uploads(bucket, key)`,
		`CREATE TABLE IF NOT EXISTS parts(
			upload_id TEXT NOT NULL,
			number INTEGER NOT NULL,
			size INTEGER NOT NULL,
			etag TEXT NOT NULL,
			blob TEXT NOT NULL,
			mtime INTEGER NOT NULL,
			PRIMARY KEY(upload_id, number))`,
	}
	for _, s := range stmts {
		if _, err := m.db.Exec(s); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}

func ms(t time.Time) int64     { return t.UnixMilli() }
func fromMS(v int64) time.Time { return time.UnixMilli(v).UTC() }
func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func encodeMeta(md map[string]string) string {
	if len(md) == 0 {
		return "{}"
	}
	b, _ := json.Marshal(md)
	return string(b)
}

func decodeMeta(s string) map[string]string {
	md := map[string]string{}
	if s != "" {
		_ = json.Unmarshal([]byte(s), &md)
	}
	return md
}

// ---------- Buckets ----------

func (m *DB) CreateBucket(ctx context.Context, b Bucket) error {
	_, err := m.db.ExecContext(ctx, `INSERT INTO buckets(name, owner, public, created_at) VALUES(?,?,?,?)`,
		b.Name, b.Owner, b2i(b.Public), ms(b.CreatedAt))
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ErrExists
	}
	return err
}

func (m *DB) GetBucket(ctx context.Context, name string) (*Bucket, error) {
	var b Bucket
	var pub int
	var created int64
	err := m.db.QueryRowContext(ctx, `SELECT name, owner, public, created_at FROM buckets WHERE name=?`, name).
		Scan(&b.Name, &b.Owner, &pub, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	b.Public = pub == 1
	b.CreatedAt = fromMS(created)
	return &b, nil
}

func (m *DB) ListBuckets(ctx context.Context) ([]Bucket, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT name, owner, public, created_at FROM buckets ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Bucket
	for rows.Next() {
		var b Bucket
		var pub int
		var created int64
		if err := rows.Scan(&b.Name, &b.Owner, &pub, &created); err != nil {
			return nil, err
		}
		b.Public = pub == 1
		b.CreatedAt = fromMS(created)
		out = append(out, b)
	}
	return out, rows.Err()
}

func (m *DB) SetBucketPublic(ctx context.Context, name string, public bool) error {
	res, err := m.db.ExecContext(ctx, `UPDATE buckets SET public=? WHERE name=?`, b2i(public), name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteBucket menghapus bucket kosong. Mengembalikan ErrNotEmpty bila masih ada objek/upload.
func (m *DB) DeleteBucket(ctx context.Context, name string) error {
	var n int
	if err := m.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM objects WHERE bucket=?) + (SELECT COUNT(*) FROM uploads WHERE bucket=?)`, name, name).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrNotEmpty
	}
	res, err := m.db.ExecContext(ctx, `DELETE FROM buckets WHERE name=?`, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

type BucketStats struct {
	Objects int64 `json:"objects"`
	Bytes   int64 `json:"bytes"`
}

func (m *DB) BucketStats(ctx context.Context, name string) (BucketStats, error) {
	var s BucketStats
	err := m.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(size),0) FROM objects WHERE bucket=?`, name).Scan(&s.Objects, &s.Bytes)
	return s, err
}

func (m *DB) TotalStats(ctx context.Context) (buckets int64, s BucketStats, err error) {
	if err = m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM buckets`).Scan(&buckets); err != nil {
		return
	}
	err = m.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(size),0) FROM objects`).Scan(&s.Objects, &s.Bytes)
	return
}

// ---------- Objects ----------

// PutObject menyisipkan/mengganti objek dan mengembalikan blob lama (bila ada) agar bisa dihapus pemanggil.
func (m *DB) PutObject(ctx context.Context, o Object) (oldBlob string, err error) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var old sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT blob FROM objects WHERE bucket=? AND key=?`, o.Bucket, o.Key).Scan(&old)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO objects(bucket,key,size,etag,content_type,metadata,blob,mtime) VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(bucket,key) DO UPDATE SET size=excluded.size, etag=excluded.etag, content_type=excluded.content_type,
		metadata=excluded.metadata, blob=excluded.blob, mtime=excluded.mtime`,
		o.Bucket, o.Key, o.Size, o.ETag, o.ContentType, encodeMeta(o.Metadata), o.Blob, ms(o.ModTime))
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	if old.Valid && old.String != o.Blob {
		return old.String, nil
	}
	return "", nil
}

func scanObject(sc interface{ Scan(...any) error }) (*Object, error) {
	var o Object
	var md string
	var mtime int64
	if err := sc.Scan(&o.Bucket, &o.Key, &o.Size, &o.ETag, &o.ContentType, &md, &o.Blob, &mtime); err != nil {
		return nil, err
	}
	o.Metadata = decodeMeta(md)
	o.ModTime = fromMS(mtime)
	return &o, nil
}

const objCols = `bucket,key,size,etag,content_type,metadata,blob,mtime`

func (m *DB) GetObject(ctx context.Context, bucket, key string) (*Object, error) {
	o, err := scanObject(m.db.QueryRowContext(ctx, `SELECT `+objCols+` FROM objects WHERE bucket=? AND key=?`, bucket, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return o, err
}

// DeleteObject menghapus baris dan mengembalikan blob yang harus dihapus. Tidak error bila tidak ada (semantik S3).
func (m *DB) DeleteObject(ctx context.Context, bucket, key string) (blob string, existed bool, err error) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `SELECT blob FROM objects WHERE bucket=? AND key=?`, bucket, key).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM objects WHERE bucket=? AND key=?`, bucket, key); err != nil {
		return "", false, err
	}
	return blob, true, tx.Commit()
}

// ListPage mengambil objek dengan key >= cursor dan berawalan prefix, urut key, maksimal limit baris.
func (m *DB) ListPage(ctx context.Context, bucket, prefix, cursor string, limit int) ([]Object, error) {
	if cursor < prefix {
		cursor = prefix
	}
	rows, err := m.db.QueryContext(ctx, `SELECT `+objCols+` FROM objects WHERE bucket=? AND key>=? AND substr(key,1,?)=? ORDER BY key LIMIT ?`,
		bucket, cursor, len(prefix), prefix, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Object
	for rows.Next() {
		o, err := scanObject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

// ListAllBlobs mengembalikan semua blob objek + part di bucket (untuk hapus paksa).
func (m *DB) BucketBlobs(ctx context.Context, bucket string) ([]string, error) {
	var out []string
	rows, err := m.db.QueryContext(ctx, `SELECT blob FROM objects WHERE bucket=? UNION ALL SELECT p.blob FROM parts p JOIN uploads u ON u.id=p.upload_id WHERE u.bucket=?`, bucket, bucket)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// PurgeBucket menghapus semua baris milik bucket (objek, upload, part, bucket itu sendiri).
func (m *DB) PurgeBucket(ctx context.Context, bucket string) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM parts WHERE upload_id IN (SELECT id FROM uploads WHERE bucket=?)`,
		`DELETE FROM uploads WHERE bucket=?`,
		`DELETE FROM objects WHERE bucket=?`,
		`DELETE FROM buckets WHERE name=?`,
	} {
		if _, err := tx.ExecContext(ctx, q, bucket); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---------- Access keys ----------

const keyChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
const secretChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

func randomString(alphabet string, n int) string {
	b := make([]byte, n)
	max := big.NewInt(int64(len(alphabet)))
	for i := range b {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic(err)
		}
		b[i] = alphabet[v.Int64()]
	}
	return string(b)
}

func NewAccessKeyID() string { return "JS" + randomString(keyChars, 18) }
func NewSecretKey() string   { return randomString(secretChars, 40) }

func (m *DB) UpsertAccessKey(ctx context.Context, k AccessKey) error {
	pol, _ := json.Marshal(k.Policies)
	if k.Policies == nil {
		pol = []byte("[]")
	}
	if k.CreatedAt.IsZero() {
		k.CreatedAt = time.Now()
	}
	_, err := m.db.ExecContext(ctx, `INSERT INTO access_keys(access_key,secret_key,name,admin,policies,enabled,created_at) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(access_key) DO UPDATE SET secret_key=excluded.secret_key, name=excluded.name, admin=excluded.admin,
		policies=excluded.policies, enabled=excluded.enabled`,
		k.AccessKey, k.SecretKey, k.Name, b2i(k.Admin), string(pol), b2i(k.Enabled), ms(k.CreatedAt))
	return err
}

func (m *DB) CreateAccessKey(ctx context.Context, k AccessKey) error {
	pol, _ := json.Marshal(k.Policies)
	if k.Policies == nil {
		pol = []byte("[]")
	}
	_, err := m.db.ExecContext(ctx, `INSERT INTO access_keys(access_key,secret_key,name,admin,policies,enabled,created_at) VALUES(?,?,?,?,?,?,?)`,
		k.AccessKey, k.SecretKey, k.Name, b2i(k.Admin), string(pol), b2i(k.Enabled), ms(k.CreatedAt))
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ErrExists
	}
	return err
}

func scanKey(sc interface{ Scan(...any) error }) (*AccessKey, error) {
	var k AccessKey
	var admin, enabled int
	var pol string
	var created int64
	if err := sc.Scan(&k.AccessKey, &k.SecretKey, &k.Name, &admin, &pol, &enabled, &created); err != nil {
		return nil, err
	}
	k.Admin = admin == 1
	k.Enabled = enabled == 1
	k.CreatedAt = fromMS(created)
	k.Policies = []Policy{}
	_ = json.Unmarshal([]byte(pol), &k.Policies)
	return &k, nil
}

const keyCols = `access_key,secret_key,name,admin,policies,enabled,created_at`

func (m *DB) GetAccessKey(ctx context.Context, ak string) (*AccessKey, error) {
	k, err := scanKey(m.db.QueryRowContext(ctx, `SELECT `+keyCols+` FROM access_keys WHERE access_key=?`, ak))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return k, err
}

func (m *DB) ListAccessKeys(ctx context.Context) ([]AccessKey, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT `+keyCols+` FROM access_keys ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccessKey
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *k)
	}
	return out, rows.Err()
}

func (m *DB) UpdateAccessKey(ctx context.Context, ak string, name *string, policies []Policy, enabled *bool, admin *bool) error {
	k, err := m.GetAccessKey(ctx, ak)
	if err != nil {
		return err
	}
	if name != nil {
		k.Name = *name
	}
	if policies != nil {
		k.Policies = policies
	}
	if enabled != nil {
		k.Enabled = *enabled
	}
	if admin != nil {
		k.Admin = *admin
	}
	return m.UpsertAccessKey(ctx, *k)
}

func (m *DB) DeleteAccessKey(ctx context.Context, ak string) error {
	res, err := m.db.ExecContext(ctx, `DELETE FROM access_keys WHERE access_key=?`, ak)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------- Multipart ----------

func (m *DB) CreateUpload(ctx context.Context, u Upload) error {
	_, err := m.db.ExecContext(ctx, `INSERT INTO uploads(id,bucket,key,content_type,metadata,created_at) VALUES(?,?,?,?,?,?)`,
		u.ID, u.Bucket, u.Key, u.ContentType, encodeMeta(u.Metadata), ms(u.CreatedAt))
	return err
}

func (m *DB) GetUpload(ctx context.Context, bucket, key, id string) (*Upload, error) {
	var u Upload
	var md string
	var created int64
	err := m.db.QueryRowContext(ctx, `SELECT id,bucket,key,content_type,metadata,created_at FROM uploads WHERE id=? AND bucket=? AND key=?`, id, bucket, key).
		Scan(&u.ID, &u.Bucket, &u.Key, &u.ContentType, &md, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.Metadata = decodeMeta(md)
	u.CreatedAt = fromMS(created)
	return &u, nil
}

func (m *DB) ListUploads(ctx context.Context, bucket, prefix string) ([]Upload, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT id,bucket,key,content_type,metadata,created_at FROM uploads WHERE bucket=? AND substr(key,1,?)=? ORDER BY key, created_at`, bucket, len(prefix), prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Upload
	for rows.Next() {
		var u Upload
		var md string
		var created int64
		if err := rows.Scan(&u.ID, &u.Bucket, &u.Key, &u.ContentType, &md, &created); err != nil {
			return nil, err
		}
		u.Metadata = decodeMeta(md)
		u.CreatedAt = fromMS(created)
		out = append(out, u)
	}
	return out, rows.Err()
}

// PutPart menyimpan part dan mengembalikan blob lama bila part diunggah ulang.
func (m *DB) PutPart(ctx context.Context, p Part) (oldBlob string, err error) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var old sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT blob FROM parts WHERE upload_id=? AND number=?`, p.UploadID, p.Number).Scan(&old)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO parts(upload_id,number,size,etag,blob,mtime) VALUES(?,?,?,?,?,?)
		ON CONFLICT(upload_id,number) DO UPDATE SET size=excluded.size, etag=excluded.etag, blob=excluded.blob, mtime=excluded.mtime`,
		p.UploadID, p.Number, p.Size, p.ETag, p.Blob, ms(p.ModTime))
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	if old.Valid {
		return old.String, nil
	}
	return "", nil
}

func (m *DB) ListParts(ctx context.Context, uploadID string) ([]Part, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT upload_id,number,size,etag,blob,mtime FROM parts WHERE upload_id=? ORDER BY number`, uploadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Part
	for rows.Next() {
		var p Part
		var mtime int64
		if err := rows.Scan(&p.UploadID, &p.Number, &p.Size, &p.ETag, &p.Blob, &mtime); err != nil {
			return nil, err
		}
		p.ModTime = fromMS(mtime)
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeleteUpload menghapus upload beserta part-nya; mengembalikan blob part untuk dihapus dari disk.
func (m *DB) DeleteUpload(ctx context.Context, uploadID string) ([]string, error) {
	parts, err := m.ListParts(ctx, uploadID)
	if err != nil {
		return nil, err
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM parts WHERE upload_id=?`, uploadID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM uploads WHERE id=?`, uploadID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	blobs := make([]string, 0, len(parts))
	for _, p := range parts {
		blobs = append(blobs, p.Blob)
	}
	return blobs, nil
}

// ListUploadsOlderThan mengembalikan upload multipart yang dimulai sebelum waktu tertentu (untuk pembersihan).
func (m *DB) ListUploadsOlderThan(ctx context.Context, before time.Time) ([]Upload, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT id,bucket,key,content_type,metadata,created_at FROM uploads WHERE created_at < ? ORDER BY created_at LIMIT 500`, ms(before))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Upload
	for rows.Next() {
		var u Upload
		var md string
		var created int64
		if err := rows.Scan(&u.ID, &u.Bucket, &u.Key, &u.ContentType, &md, &created); err != nil {
			return nil, err
		}
		u.Metadata = decodeMeta(md)
		u.CreatedAt = fromMS(created)
		out = append(out, u)
	}
	return out, rows.Err()
}
