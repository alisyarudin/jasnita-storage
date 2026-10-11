<?php
/**
 * Uji end-to-end memakai aws/aws-sdk-php + league/flysystem-aws-s3-v3 persis seperti Laravel.
 * Jalankan dari folder proyek Laravel yang punya vendor/:
 *   php /path/jasnita-storage/scripts/laravel-check.php http://127.0.0.1:9000 rootkey rootsecret123
 */
[$_, $endpoint, $ak, $sk] = $argv + [null, 'http://127.0.0.1:9000', 'rootkey', 'rootsecret123'];
$autoload = getcwd() . '/vendor/autoload.php';
if (!file_exists($autoload)) { fwrite(STDERR, "Jalankan dari folder Laravel (vendor/autoload.php tidak ada)\n"); exit(1); }
require $autoload;

use Aws\S3\S3Client;
use League\Flysystem\AwsS3V3\AwsS3V3Adapter;
use League\Flysystem\Filesystem;

$bucket = 'e2e-php-' . mt_rand(1000, 9999);
$client = new S3Client([
    'version' => 'latest', 'region' => 'us-east-1', 'endpoint' => $endpoint, 'use_path_style_endpoint' => true,
    'credentials' => ['key' => $ak, 'secret' => $sk],
]);
function ok($label, $cond) { if (!$cond) { fwrite(STDERR, "GAGAL: $label\n"); exit(1); } echo "ok  $label\n"; }

$client->createBucket(['Bucket' => $bucket]);
ok('createBucket', $client->doesBucketExistV2($bucket, false));

$fs = new Filesystem(new AwsS3V3Adapter($client, $bucket));
$fs->write('dokumen/halo.txt', 'halo dari laravel', ['visibility' => 'private', 'ContentType' => 'text/plain']);
ok('write', $fs->read('dokumen/halo.txt') === 'halo dari laravel');
ok('fileExists', $fs->fileExists('dokumen/halo.txt'));
ok('mimeType', $fs->mimeType('dokumen/halo.txt') === 'text/plain');
ok('fileSize', $fs->fileSize('dokumen/halo.txt') === 17);

// stream besar 20MB -> S3Client::upload memakai multipart (threshold 16MB)
$tmp = tmpfile();
for ($i = 0; $i < 20; $i++) fwrite($tmp, random_bytes(1024 * 1024));
rewind($tmp);
$fs->writeStream('besar.bin', $tmp);
ok('writeStream multipart 20MB', $fs->fileSize('besar.bin') === 20 * 1024 * 1024);
rewind($tmp);
$remote = $fs->readStream('besar.bin');
ok('readStream isi sama', hash('sha256', stream_get_contents($remote)) === hash('sha256', stream_get_contents($tmp)));

$fs->copy('dokumen/halo.txt', 'dokumen/salinan.txt');
$fs->move('dokumen/salinan.txt', 'dokumen/pindah.txt');
ok('copy+move', $fs->read('dokumen/pindah.txt') === 'halo dari laravel' && !$fs->fileExists('dokumen/salinan.txt'));

$fs->createDirectory('kosong');
$list = iterator_to_array($fs->listContents('', false));
$names = array_map(fn($i) => $i->path(), $list);
sort($names);
ok('listContents root', $names === ['besar.bin', 'dokumen', 'kosong']);
$deep = array_map(fn($i) => $i->path(), iterator_to_array($fs->listContents('', true)));
ok('listContents rekursif', in_array('dokumen/pindah.txt', $deep, true));

$cmd = $client->getCommand('GetObject', ['Bucket' => $bucket, 'Key' => 'dokumen/halo.txt', 'ResponseContentDisposition' => 'attachment; filename="halo.txt"']);
$url = (string) $client->createPresignedRequest($cmd, '+5 minutes')->getUri();
$ctx = stream_context_create(['http' => ['ignore_errors' => true]]);
$body = file_get_contents($url, false, $ctx);
ok('presigned GET (temporaryUrl)', $body === 'halo dari laravel' && str_contains(implode("\n", $http_response_header), 'attachment'));

$cmd = $client->getCommand('PutObject', ['Bucket' => $bucket, 'Key' => 'unggah-browser.txt', 'ContentType' => 'text/plain']);
$url = (string) $client->createPresignedRequest($cmd, '+5 minutes')->getUri();
$ctx = stream_context_create(['http' => ['method' => 'PUT', 'header' => "Content-Type: text/plain\r\n", 'content' => 'dari browser', 'ignore_errors' => true]]);
file_get_contents($url, false, $ctx);
ok('presigned PUT', $fs->read('unggah-browser.txt') === 'dari browser');

$fs->delete('dokumen/halo.txt');
$fs->deleteDirectory('dokumen');
ok('delete + deleteDirectory', !$fs->fileExists('dokumen/pindah.txt'));
$fs->delete('besar.bin');
$fs->delete('unggah-browser.txt');
$fs->deleteDirectory('kosong');
ok('bucket kosong', iterator_to_array($fs->listContents('', true)) === []);
$client->deleteBucket(['Bucket' => $bucket]);
ok('deleteBucket', !$client->doesBucketExistV2($bucket, false));
echo "SEMUA UJI LARAVEL/SDK PHP LOLOS\n";
