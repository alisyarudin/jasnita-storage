# JasStore

Object storage S3-compatible buatan sendiri (ala MinIO) dengan web console. Satu binary Go,
data di disk lokal, metadata di SQLite. Dipakai Laravel lewat disk `s3` bawaan.

```
┌──────────────┐   S3 API (SigV4)   ┌──────────────────────────┐
│ Laravel /    │ ─────────────────▶ │ jasstore :9000  (S3 API) │   data/<bucket>/xx/<id>
│ aws cli /    │                    │          :9001  (console)│   data/jasstore.db (SQLite)
│ browser      │ ◀───────────────── │                          │
└──────────────┘  presigned URL     └──────────────────────────┘
```

## Fitur

- Bucket: buat/hapus/list, bucket publik (GET anonim), virtual-host style opsional
- Objek: PUT/GET/HEAD/DELETE/COPY, Range, metadata, DeleteObjects, ListObjects v1 & v2 (prefix, delimiter, paginasi)
- Multipart upload (dipakai otomatis oleh Laravel/aws cli untuk berkas besar)
- AWS Signature V4: header, presigned URL, aws-chunked streaming (diuji dengan vektor resmi AWS)
- Access key per aplikasi dengan izin per bucket (baca / baca+tulis / admin)
- Web console: dashboard, browser bucket (unggah drag-drop, unduh, folder, hapus), kelola access key
- Pembersihan otomatis multipart upload yang terbengkalai (> 7 hari)

## Menjalankan secara lokal

```bash
make web      # build console (butuh Node 20)
make build    # bin/jasstore
cp .env.example .env   # ubah JASSTORE_ROOT_*
./bin/jasstore
```

- API S3: http://127.0.0.1:9000
- Console: http://127.0.0.1:9001 (login dengan root access key / secret dari `.env`)

Uji:

```bash
go test ./...
ENDPOINT=http://127.0.0.1:9000 AK=jasstore SK=... scripts/e2e-awscli.sh
cd ../clicktocall-v3 && php ../jasnita-storage/scripts/laravel-check.php http://127.0.0.1:9000 jasstore ...
```

## Deploy ke server (systemd + nginx)

```bash
make web build-linux
scp bin/jasstore-linux-amd64 deploy/jasstore.service deploy/install.sh server:/tmp/
ssh server 'cd /tmp && sudo ./install.sh ./jasstore-linux-amd64'
# nginx: deploy/nginx-jasstore.conf (s3.jascall.com -> 9000, storage.jascall.com -> 9001)
```

Catatan nginx: `proxy_set_header Host $http_host` wajib (host ikut ditandatangani), `client_max_body_size 0`
dan `proxy_request_buffering off` supaya upload besar di-stream.

Atau Docker: `docker compose up -d`.

## Konfigurasi (env)

| Variabel | Default | Keterangan |
|---|---|---|
| `JASSTORE_DATA_DIR` | `./data` | direktori objek + `jasstore.db` |
| `JASSTORE_DB_PATH` | `<data>/jasstore.db` | lokasi SQLite |
| `JASSTORE_S3_ADDR` | `:9000` | listen API S3 |
| `JASSTORE_CONSOLE_ADDR` | `:9001` | listen console |
| `JASSTORE_REGION` | `us-east-1` | region yang dilaporkan |
| `JASSTORE_PUBLIC_URL` | (otomatis) | URL endpoint S3 untuk contoh di console |
| `JASSTORE_DOMAIN` | kosong | aktifkan `bucket.<domain>` |
| `JASSTORE_ROOT_ACCESS_KEY` / `JASSTORE_ROOT_SECRET_KEY` | wajib | admin root |

## Pakai dari Laravel

Lihat [docs/laravel.md](docs/laravel.md). Ringkasnya:

```env
AWS_ENDPOINT=https://s3.jascall.com
AWS_USE_PATH_STYLE_ENDPOINT=true
AWS_BUCKET=nama-bucket
AWS_ACCESS_KEY_ID=...
AWS_SECRET_ACCESS_KEY=...
```

## Tata letak kode

- `cmd/jasstore` – entry point, dua server HTTP (S3 + console), GC multipart
- `internal/sigv4` – verifikasi Signature V4 (header, presigned, aws-chunked) + penanda tangan untuk tes
- `internal/s3api` – handler S3 (bucket, objek, listing, multipart), error XML
- `internal/meta` – SQLite: bucket, objek, access key, upload/part
- `internal/blob` – berkas fisik (tulis atomik via tmp+rename, md5/sha256 saat tulis)
- `internal/console` – admin API JSON + console Vue tertanam (`dist/`)
- `web/` – sumber console (Vue 3 + Vite)

## Backup

Hentikan tulis sebentar atau pakai snapshot: salin seluruh `JASSTORE_DATA_DIR` (berkas objek + `jasstore.db`).
SQLite bisa di-backup online dengan `sqlite3 jasstore.db ".backup b.db"`.

## Batasan saat ini

Tanpa versioning, lifecycle, bucket policy JSON, tagging, UploadPartCopy, enkripsi sisi server,
dan tanpa replikasi multi-node. Satu proses, satu disk.
