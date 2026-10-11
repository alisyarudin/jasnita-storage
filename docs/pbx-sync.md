# Sinkronisasi rekaman PBX ke JasStore

Rekaman Asterisk/FreePBX (`/var/spool/asterisk/monitor/YYYY/MM/DD/*.wav`) diunggah otomatis ke bucket
JasStore memakai **rclone** yang dijalankan systemd timer tiap menit. Struktur folder dipertahankan:

```
PBX:   /var/spool/asterisk/monitor/2026/10/11/out-0812-1001-20261011-100000-1760000000.1.wav
S3:    s3://recordings/pbx-sip1/2026/10/11/out-0812-1001-20261011-100000-1760000000.1.wav
```

Diuji dengan rclone 1.75: copy, multipart (> 16 MB), `rclone check` (md5 cocok), `move`.

## Sudah pakai `mc` + inotifywait? (callcenter-api-go)

PBX yang sudah memakai pipeline dari `elastix-api/callcenter-api-go/docs/minio-recording-sync-setup.md`
(inotifywait → `mc cp`, backfill `mc mirror`, API fallback presigned ke StorageRaya) **tidak perlu rclone**.
`mc` dan `minio-go` v7.1.0 sudah diuji penuh ke JasStore (cp, mirror, stat, share, rb --force, presigned).
Cukup arahkan ke JasStore:

```bash
# di PBX, sebagai user yang menjalankan asterisk-recording-sync.service
mc alias set storageraya https://s3.jascall.com <ACCESS_KEY> <SECRET_KEY>   # nama alias dibiarkan, isinya diganti
mc mb storageraya/pbx-recording
systemctl restart asterisk-recording-sync.service
mc mirror /var/spool/asterisk/monitor/ storageraya/pbx-recording/recordings/${SERVER_CODE}/   # backfill
```

Lalu di `callcenter-api-<pbx>.conf` bagian `[minio]`: `endpoint = s3.jascall.com`, `use_ssl = yes`,
`access_key`/`secret_key` dari JasStore, `bucket`/`prefix` tetap. Restart `callcenter-api`.
Tata letak key `recordings/<server_code>/monitor/YYYY/MM/DD/file.wav` tidak berubah, jadi `minioObjectKey()` di API tidak perlu disentuh.

## Dua mode

| Mode | Perilaku | Cocok untuk |
|---|---|---|
| `copy` (default) | salin ke storage; lokal dihapus hanya bila > `KEEP_DAYS` hari **dan** sudah terbukti ada di storage | PBX yang masih memutar rekaman dari disk lokal |
| `move` | pindahkan; lokal dihapus begitu unggahan sukses | PBX disk kecil, pemutaran lewat portal (presigned URL) |

`MIN_AGE=2m` memastikan berkas yang masih direkam (masih ditulis Asterisk) tidak ikut.

## Pasang di PBX

Di JasStore console: buat bucket `recordings` dan access key `pbx-sip1` dengan izin `recordings: baca + tulis`.

```bash
# dari Mac
scp -r deploy/pbx root@PBX:/tmp/jasstore-pbx
# di PBX (root)
cd /tmp/jasstore-pbx && ./install-pbx.sh
vi /etc/jasstore/rclone.conf     # access key, secret, endpoint https://s3.jascall.com
vi /etc/jasstore/sync.env        # SOURCE_DIR, DEST=jasstore:recordings/pbx-sip1, MODE
rclone --config /etc/jasstore/rclone.conf lsd jasstore:     # harus menampilkan bucket
systemctl start jasstore-sync.service && tail -f /var/log/jasstore-sync.log
```

Timer: `systemctl list-timers jasstore-sync.timer`. Log: `/var/log/jasstore-sync.log`.
Satu PBX = satu prefix (`pbx-sip1`, `pbx-mandiri`, ...) agar nama berkas tidak bentrok.

## Unggah seketika setelah panggilan (opsional)

Timer menyusul paling lama ±3 menit setelah panggilan (MIN_AGE + interval). Bila portal perlu rekaman
langsung tersedia, tambahkan hook post-recording. FreePBX: *Settings → Advanced Settings → Post Call Recording Script*:

```
/usr/local/bin/jasstore-upload-call.sh ^{MIXMONITOR_FILENAME}
```

Dialplan murni: `MixMonitor(${REC},b,/usr/local/bin/jasstore-upload-call.sh ^{MIXMONITOR_FILENAME})`.
Hook ini berjalan di latar belakang dan timer tetap menjadi jaring pengaman.

## Sisi Laravel (clicktocall-v3)

Tambah disk di `config/filesystems.php`:

```php
'recordings' => [
    'driver' => 's3',
    'key' => env('AWS_ACCESS_KEY_ID'),
    'secret' => env('AWS_SECRET_ACCESS_KEY'),
    'region' => env('AWS_DEFAULT_REGION', 'ap-southeast-1'),
    'bucket' => 'recordings',
    'endpoint' => env('AWS_ENDPOINT'),
    'use_path_style_endpoint' => true,
    'throw' => false,
],
```

Key objek bisa diturunkan dari kolom rekaman yang sudah ada (path lokal PBX):

```php
// /var/spool/asterisk/monitor/2026/10/11/x.wav  ->  pbx-sip1/2026/10/11/x.wav
$key = 'pbx-sip1/' . Str::after($row->recording_file, '/var/spool/asterisk/monitor/');
$url = Storage::disk('recordings')->temporaryUrl($key, now()->addMinutes(15));   // untuk player
Storage::disk('recordings')->exists($key);                                        // sudah tersinkron?
```

Key access untuk v3 cukup `recordings: baca` kalau portal hanya memutar/mengunduh.

## Pemantauan

- `grep -c ERROR /var/log/jasstore-sync.log` naik → cek kredensial/jaringan.
- `rclone --config /etc/jasstore/rclone.conf check /var/spool/asterisk/monitor jasstore:recordings/pbx-sip1 --one-way`
  memastikan semua rekaman lokal sudah ada di storage.
- Dashboard JasStore menampilkan ukuran bucket dan sisa disk.
