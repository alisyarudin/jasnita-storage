<script setup>
import { ref, onMounted, computed } from 'vue'
import { api, fmtBytes, fmtNum, auth } from '../api.js'

const stats = ref(null)
const cfg = ref(null)
const buckets = ref([])
const error = ref('')

const diskPct = computed(() => stats.value?.disk?.total ? Math.round(stats.value.disk.used / stats.value.disk.total * 100) : 0)
const diskClass = computed(() => diskPct.value >= 90 ? 'danger' : diskPct.value >= 75 ? 'warn' : '')

onMounted(async () => {
  try {
    ;[stats.value, cfg.value, buckets.value] = await Promise.all([api('GET', '/api/stats'), api('GET', '/api/config'), api('GET', '/api/buckets')])
  } catch (e) { error.value = e.message }
})

const laravelEnv = computed(() => `AWS_ACCESS_KEY_ID=${auth.user?.accessKey || 'ACCESS_KEY'}
AWS_SECRET_ACCESS_KEY=SECRET_KEY
AWS_DEFAULT_REGION=${cfg.value?.region || 'us-east-1'}
AWS_BUCKET=${buckets.value[0]?.name || 'nama-bucket'}
AWS_ENDPOINT=${cfg.value?.endpoint || 'http://127.0.0.1:9000'}
AWS_USE_PATH_STYLE_ENDPOINT=true`)
</script>

<template>
  <div class="page-head">
    <div><h1>Dashboard</h1><p>Ringkasan penyimpanan · JasStore {{ cfg?.version }}</p></div>
  </div>
  <div v-if="error" class="alert error">{{ error }}</div>

  <div class="tiles">
    <div class="tile"><div class="label">Bucket</div><div class="value">{{ fmtNum(stats?.buckets) }}</div></div>
    <div class="tile"><div class="label">Objek</div><div class="value">{{ fmtNum(stats?.objects) }}</div></div>
    <div class="tile"><div class="label">Data tersimpan</div><div class="value">{{ fmtBytes(stats?.bytes) }}</div></div>
    <div class="tile">
      <div class="label">Disk terpakai</div>
      <div class="value">{{ diskPct }}%</div>
      <div class="meter" :class="diskClass" role="meter" :aria-valuenow="diskPct" aria-valuemin="0" aria-valuemax="100"><i :style="{ width: diskPct + '%' }"></i></div>
      <div class="sub">{{ fmtBytes(stats?.disk?.used) }} dari {{ fmtBytes(stats?.disk?.total) }} · sisa {{ fmtBytes(stats?.disk?.free) }}</div>
    </div>
  </div>

  <div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(320px,1fr));gap:16px">
    <div class="card">
      <h2>Bucket terbesar</h2>
      <table v-if="buckets.length">
        <thead><tr><th>Nama</th><th class="num">Objek</th><th class="num">Ukuran</th></tr></thead>
        <tbody>
          <tr v-for="b in [...buckets].sort((a, c) => c.bytes - a.bytes).slice(0, 8)" :key="b.name" class="row">
            <td><router-link :to="'/buckets/' + b.name">{{ b.name }}</router-link> <span v-if="b.public" class="badge soft">publik</span></td>
            <td class="num">{{ fmtNum(b.objects) }}</td>
            <td class="num">{{ fmtBytes(b.bytes) }}</td>
          </tr>
        </tbody>
      </table>
      <div v-else class="empty">Belum ada bucket. <router-link to="/buckets">Buat bucket</router-link>.</div>
    </div>
    <div class="card">
      <h2>Pakai di Laravel</h2>
      <p class="muted" style="margin:0 0 8px">Endpoint S3: <code>{{ cfg?.endpoint }}</code> · region <code>{{ cfg?.region }}</code>. Isi <code>.env</code> Laravel:</p>
      <pre>{{ laravelEnv }}</pre>
      <p class="muted" style="margin:8px 0 0">Lalu <code>Storage::disk('s3')->put('folder/berkas.txt', $isi)</code> langsung jalan. Secret key hanya ditampilkan saat key dibuat.</p>
    </div>
  </div>
</template>
