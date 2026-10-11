# Deploy JasStore ke server (Ubuntu + systemd + nginx)

Contoh di bawah memakai server VOIPCC `163.7.2.210` (SSH port 12777), domain
`s3.jascall.com` untuk API S3 dan `storage.jascall.com` untuk console. Ganti sesuai kebutuhan.

## 0. Siapkan DNS dulu

Buat dua record A ke IP server:

| Nama | Tipe | Nilai | Cloudflare proxy |
|---|---|---|---|
| `s3.jascall.com` | A | 163.7.2.210 | **DNS only (awan abu-abu)** |
| `storage.jascall.com` | A | 163.7.2.210 | boleh proxied |

`s3` jangan lewat proxy Cloudflare: Cloudflare membatasi upload 100 MB per request dan
timeout 100 detik, sehingga unggahan rekaman/backup besar akan gagal.

## 1. Build di Mac

```bash
cd ~/Project/Website/jasnita-storage
source ~/.nvm/nvm.sh && nvm use 20
make web build-linux          # hasil: bin/jasstore-linux-amd64
```

## 2. Salin ke server

```bash
scp -i ~/.ssh/id_ed25519 -P 12777 \
  bin/jasstore-linux-amd64 deploy/install.sh deploy/jasstore.service deploy/nginx-jasstore.conf \
  root@163.7.2.210:/tmp/
```

## 3. Pasang service

```bash
ssh -i ~/.ssh/id_ed25519 -p 12777 root@163.7.2.210
cd /tmp && chmod +x install.sh && ./install.sh ./jasstore-linux-amd64
```

Skrip ini membuat user `jasstore`, folder data `/var/lib/jasstore`, berkas env
`/etc/jasstore/jasstore.env` (root secret acak dibuat otomatis), memasang unit systemd,
lalu menyalakannya. Cek:

```bash
systemctl status jasstore --no-pager
journalctl -u jasstore -n 20 --no-pager
cat /etc/jasstore/jasstore.env        # catat JASSTORE_ROOT_ACCESS_KEY / SECRET
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:9001/   # 200
curl -s http://127.0.0.1:9000/                                     # XML AccessDenied = normal
```

Kalau mau ganti region atau URL publik, edit env lalu `systemctl restart jasstore`.
Service hanya mendengarkan di `127.0.0.1`, jadi tidak terbuka ke internet tanpa nginx.

## 4. nginx + SSL

```bash
cp /tmp/nginx-jasstore.conf /etc/nginx/sites-available/jasstore
ln -s /etc/nginx/sites-available/jasstore /etc/nginx/sites-enabled/jasstore
```

Sertifikat: certbot sudah terpasang di server ini. Ambil cert untuk dua domain
(sementara hapus/komentari baris `listen 443 ssl` di conf jika certbot menolak karena cert belum ada,
atau pakai mode webroot):

```bash
certbot --nginx -d s3.jascall.com -d storage.jascall.com
nginx -t && systemctl reload nginx
```

Setelah certbot, pastikan di blok `s3.jascall.com` masih ada:

```
client_max_body_size 0;
proxy_request_buffering off;
proxy_set_header Host $http_host;
```

Tanpa `Host $http_host`, semua request S3 ditolak `SignatureDoesNotMatch` karena host ikut ditandatangani.

Catatan untuk server ini: ada `conf.d/03-default-deny.conf` (catch-all 444) dan
`conf.d/00-ratelimit.conf`. Rate limit 30 r/s berlaku per `location /` di vhost voipcc saja,
jadi tidak memengaruhi vhost jasstore. Simpan salinan conf yang dipakai ke `docs/nginx/` repo v3
seperti kebiasaan.

## 5. Uji dari Mac

```bash
export AWS_ACCESS_KEY_ID=<root access key> AWS_SECRET_ACCESS_KEY=<root secret> AWS_DEFAULT_REGION=ap-southeast-1
aws --endpoint-url https://s3.jascall.com s3 mb s3://uji
echo halo | aws --endpoint-url https://s3.jascall.com s3 cp - s3://uji/halo.txt
aws --endpoint-url https://s3.jascall.com s3 ls s3://uji/
aws --endpoint-url https://s3.jascall.com s3 rb s3://uji --force
```

Uji lengkap (multipart, presigned, SDK PHP):

```bash
ENDPOINT=https://s3.jascall.com AK=... SK=... scripts/e2e-awscli.sh
cd ../clicktocall-v3 && php ../jasnita-storage/scripts/laravel-check.php https://s3.jascall.com AK SK
```

## 6. Buat bucket dan key untuk aplikasi

Buka https://storage.jascall.com, login dengan root key dari `/etc/jasstore/jasstore.env`:

1. **Bucket → + Bucket baru**: misalnya `clicktocall` (privat).
2. **Access Key → + Key baru**: nama `clicktocall-v3`, izin `clicktocall: baca + tulis`. Simpan secret.
3. Isi `.env` Laravel di server:

```env
AWS_ACCESS_KEY_ID=JS...
AWS_SECRET_ACCESS_KEY=...
AWS_DEFAULT_REGION=ap-southeast-1
AWS_BUCKET=clicktocall
AWS_ENDPOINT=https://s3.jascall.com
AWS_USE_PATH_STYLE_ENDPOINT=true
```

lalu `php artisan config:clear`. Jangan pakai root key di aplikasi.

## 7. Update versi

```bash
# Mac
make web build-linux
scp -i ~/.ssh/id_ed25519 -P 12777 bin/jasstore-linux-amd64 root@163.7.2.210:/tmp/
# server
install -m 0755 /tmp/jasstore-linux-amd64 /usr/local/bin/jasstore && systemctl restart jasstore
```

Database SQLite dimigrasi otomatis saat start.

## 8. Backup

```bash
# snapshot metadata tanpa menghentikan service
sqlite3 /var/lib/jasstore/jasstore.db ".backup /root/jasstore-$(date +%F).db"
# objek: rsync folder data ke tempat lain
rsync -a /var/lib/jasstore/ backup-host:/backup/jasstore/
```

Pulihkan dengan menyalin balik folder `/var/lib/jasstore` (objek + db) lalu `systemctl restart jasstore`.

## Troubleshooting

| Gejala | Penyebab umum |
|---|---|
| `SignatureDoesNotMatch` lewat nginx, tapi lokal oke | `proxy_set_header Host $http_host` hilang, atau Cloudflare mengubah header |
| `RequestTimeTooSkewed` | jam server melenceng > 15 menit; cek `chronyc tracking` (pernah terjadi di server ini) |
| Upload besar putus di ~100 MB | subdomain `s3` masih proxied Cloudflare |
| `413 Request Entity Too Large` | `client_max_body_size` belum 0 di vhost jasstore |
| `InvalidAccessKeyId` padahal key benar | key dinonaktifkan di console, atau salah endpoint (server lain) |
| Console 502 | service mati: `journalctl -u jasstore -n 50` |
