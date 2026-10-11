#!/usr/bin/env bash
# Uji end-to-end dengan aws cli terhadap server jasstore.
# Pakai: ENDPOINT=http://127.0.0.1:9000 AK=... SK=... scripts/e2e-awscli.sh
set -euo pipefail
ENDPOINT=${ENDPOINT:-http://127.0.0.1:9000}
export AWS_ACCESS_KEY_ID=${AK:-rootkey} AWS_SECRET_ACCESS_KEY=${SK:-rootsecret123} AWS_DEFAULT_REGION=${REGION:-us-east-1}
export AWS_EC2_METADATA_DISABLED=true
B=e2e-cli-$RANDOM
T=$(mktemp -d)
cli() { aws --endpoint-url "$ENDPOINT" "$@"; }

echo "== buat bucket $B"; cli s3 mb "s3://$B"
echo "== list buckets"; cli s3 ls | grep "$B" >/dev/null
echo "== upload kecil"; echo "halo dari cli" > "$T/kecil.txt"; cli s3 cp "$T/kecil.txt" "s3://$B/folder a/kecil.txt"
echo "== upload besar (multipart 20MB)"; head -c 20971520 /dev/urandom > "$T/besar.bin"; cli s3 cp "$T/besar.bin" "s3://$B/besar.bin"
echo "== ls"; cli s3 ls "s3://$B/" ; cli s3 ls "s3://$B/folder a/"
echo "== download & bandingkan"; cli s3 cp "s3://$B/besar.bin" "$T/besar2.bin"; cmp "$T/besar.bin" "$T/besar2.bin"
cli s3 cp "s3://$B/folder a/kecil.txt" "$T/kecil2.txt"; cmp "$T/kecil.txt" "$T/kecil2.txt"
echo "== head-object"; cli s3api head-object --bucket "$B" --key besar.bin | grep -q '"ContentLength": 20971520'
echo "== copy & move"; cli s3 cp "s3://$B/folder a/kecil.txt" "s3://$B/salinan.txt"; cli s3 mv "s3://$B/salinan.txt" "s3://$B/pindah.txt"
echo "== presign"; URL=$(cli s3 presign "s3://$B/pindah.txt" --expires-in 60); curl -sf "$URL" | grep -q "halo dari cli"
echo "== sync folder"; mkdir -p "$T/sync/sub"; echo a > "$T/sync/a.txt"; echo b > "$T/sync/sub/b.txt"; cli s3 sync "$T/sync" "s3://$B/sync/"; cli s3 ls "s3://$B/sync/" --recursive | grep -q sub/b.txt
echo "== list-objects-v2 paginasi"; [ "$(cli s3api list-objects-v2 --bucket "$B" --page-size 2 --query 'length(Contents)')" = "6" ]
echo "== hapus rekursif"; cli s3 rm "s3://$B/" --recursive
echo "== hapus bucket"; cli s3 rb "s3://$B"
rm -rf "$T"
echo "SEMUA UJI AWS CLI LOLOS"
