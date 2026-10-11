# Memakai JasStore dari Laravel

JasStore bicara protokol S3, jadi Laravel memakai driver `s3` bawaan tanpa package tambahan.
Paket yang dibutuhkan (sudah ada di clicktocall-v3): `league/flysystem-aws-s3-v3`.

## 1. Buat bucket dan access key

Di console (`https://storage.jascall.com`):

1. **Bucket → + Bucket baru**, misal `clicktocall`. Centang *Publik* hanya bila berkas boleh diunduh siapa pun lewat URL biasa.
2. **Access Key → + Key baru**, nama `clicktocall-v3`, izin `clicktocall: baca + tulis`. Salin access key dan secret key (secret hanya tampil sekali).

Atau lewat kode (admin key) saat provisioning:

```php
use Aws\S3\S3Client;

$client = new S3Client([
    'version' => 'latest',
    'region' => env('AWS_DEFAULT_REGION'),
    'endpoint' => env('AWS_ENDPOINT'),
    'use_path_style_endpoint' => true,
    'credentials' => ['key' => env('AWS_ACCESS_KEY_ID'), 'secret' => env('AWS_SECRET_ACCESS_KEY')],
]);
$client->createBucket(['Bucket' => 'tenant-123']);
```

## 2. Konfigurasi `.env`

```env
FILESYSTEM_DISK=s3
AWS_ACCESS_KEY_ID=JSXXXXXXXXXXXXXXXXXX
AWS_SECRET_ACCESS_KEY=xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
AWS_DEFAULT_REGION=ap-southeast-1
AWS_BUCKET=clicktocall
AWS_ENDPOINT=https://s3.jascall.com
AWS_USE_PATH_STYLE_ENDPOINT=true
```

`config/filesystems.php` bawaan Laravel sudah membaca `AWS_ENDPOINT` dan `AWS_USE_PATH_STYLE_ENDPOINT`;
`AWS_URL` boleh diisi `https://s3.jascall.com/clicktocall` agar `Storage::url()` menghasilkan URL yang benar untuk bucket publik.

Beberapa disk sekaligus (mis. rekaman terpisah):

```php
'recordings' => [
    'driver' => 's3',
    'key' => env('AWS_ACCESS_KEY_ID'),
    'secret' => env('AWS_SECRET_ACCESS_KEY'),
    'region' => env('AWS_DEFAULT_REGION'),
    'bucket' => 'recordings',
    'endpoint' => env('AWS_ENDPOINT'),
    'use_path_style_endpoint' => true,
    'throw' => true,
],
```

## 3. Pemakaian

```php
Storage::disk('s3')->put('rekaman/2026/10/call-123.wav', $bytes);
Storage::disk('s3')->putFileAs('audio-library', $request->file('audio'), 'intro.wav');
Storage::disk('s3')->writeStream('backup/db.sql.gz', $stream);   // > 16 MB otomatis multipart
Storage::disk('s3')->get('rekaman/2026/10/call-123.wav');
Storage::disk('s3')->download('rekaman/2026/10/call-123.wav');  // stream ke browser
Storage::disk('s3')->temporaryUrl('rekaman/2026/10/call-123.wav', now()->addMinutes(10),
    ['ResponseContentDisposition' => 'attachment; filename="call-123.wav"']);
Storage::disk('s3')->files('rekaman/2026/10');
Storage::disk('s3')->deleteDirectory('rekaman/2025');
```

Presigned PUT agar browser mengunggah langsung ke storage tanpa lewat PHP:

```php
$client = Storage::disk('s3')->getClient();
$cmd = $client->getCommand('PutObject', ['Bucket' => config('filesystems.disks.s3.bucket'), 'Key' => $key, 'ContentType' => $mime]);
$url = (string) $client->createPresignedRequest($cmd, '+10 minutes')->getUri();
// kirim $url ke frontend, lalu fetch(url, { method: 'PUT', body: file, headers: { 'Content-Type': mime } })
```

## 4. Yang didukung dan yang belum

Didukung: bucket (buat/hapus/list), objek (put/get/head/delete/copy, Range, metadata `x-amz-meta-*`,
Cache-Control/Content-Disposition), DeleteObjects, ListObjects v1/v2 dengan prefix/delimiter/paginasi,
multipart upload, presigned URL (GET/PUT), aws-chunked (streaming signature), bucket publik (GET anonim),
CORS permisif untuk upload dari browser.

Belum: versioning, lifecycle/expiry otomatis, bucket policy JSON, object tagging, UploadPartCopy, SSE.
Semua panggilan ini mengembalikan `NotImplemented` (501), bukan diam-diam sukses.

## 5. Uji cepat

```bash
cd clicktocall-v3
php ../jasnita-storage/scripts/laravel-check.php https://s3.jascall.com ACCESS_KEY SECRET_KEY
```
