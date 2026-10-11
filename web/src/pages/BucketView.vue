<script setup>
import { ref, computed, watch, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, auth, downloadUrl, fmtBytes, fmtDate } from '../api.js'
import Modal from '../components/Modal.vue'

const route = useRoute()
const router = useRouter()
const bucket = computed(() => route.params.name)
const prefix = computed(() => route.query.p || '')
const items = ref([])
const next = ref(null)
const loading = ref(false)
const error = ref('')
const cfg = ref(null)
const info = ref(null)
const uploads = ref([])
const showFolder = ref(false)
const folderName = ref('')
const fileInput = ref(null)
const filter = ref('')

const crumbs = computed(() => {
  const parts = prefix.value.split('/').filter(Boolean)
  return parts.map((p, i) => ({ name: p, prefix: parts.slice(0, i + 1).join('/') + '/' }))
})
const visible = computed(() => {
  const f = filter.value.toLowerCase()
  return f ? items.value.filter(i => i.name.toLowerCase().includes(f)) : items.value
})

async function load(append = false) {
  loading.value = true
  error.value = ''
  try {
    const q = new URLSearchParams({ prefix: prefix.value })
    if (append && next.value) q.set('token', next.value)
    const r = await api('GET', `/api/buckets/${encodeURIComponent(bucket.value)}/objects?${q}`)
    items.value = append ? items.value.concat(r.items) : r.items
    next.value = r.truncated ? r.next : null
  } catch (e) { error.value = e.message } finally { loading.value = false }
}

async function loadInfo() {
  try {
    const all = await api('GET', '/api/buckets')
    info.value = all.find(b => b.name === bucket.value) || null
    cfg.value = cfg.value || await api('GET', '/api/config')
  } catch {}
}

onMounted(() => { load(); loadInfo() })
watch(() => [route.params.name, route.query.p], () => { items.value = []; load(); loadInfo() })

function go(p) { router.push({ path: `/buckets/${bucket.value}`, query: p ? { p } : {} }) }
function open(it) { it.dir ? go(it.key) : window.open(downloadUrl(bucket.value, it.key, false), '_blank') }

function uploadFiles(files) {
  for (const f of files) {
    const key = prefix.value + (f.webkitRelativePath || f.name)
    const u = { name: f.name, pct: 0, error: '' }
    uploads.value.push(u)
    const xhr = new XMLHttpRequest()
    xhr.open('PUT', `/api/buckets/${encodeURIComponent(bucket.value)}/object?key=${encodeURIComponent(key)}`)
    xhr.setRequestHeader('Authorization', 'Bearer ' + auth.token)
    if (f.type) xhr.setRequestHeader('Content-Type', f.type)
    xhr.upload.onprogress = e => { if (e.lengthComputable) u.pct = Math.round(e.loaded / e.total * 100) }
    xhr.onload = () => {
      if (xhr.status >= 300) { try { u.error = JSON.parse(xhr.responseText).error } catch { u.error = 'gagal (' + xhr.status + ')' } }
      else { u.pct = 100; setTimeout(() => uploads.value.splice(uploads.value.indexOf(u), 1), 1500) }
      load(); loadInfo()
    }
    xhr.onerror = () => { u.error = 'koneksi gagal' }
    xhr.send(f)
  }
}
function onPick(e) { uploadFiles(e.target.files); e.target.value = '' }
function onDrop(e) { if (e.dataTransfer?.files?.length) uploadFiles(e.dataTransfer.files) }

async function createFolder() {
  if (!folderName.value.trim()) return
  try {
    await api('POST', `/api/buckets/${encodeURIComponent(bucket.value)}/folder`, { prefix: prefix.value + folderName.value.trim() })
    showFolder.value = false; folderName.value = ''; load()
  } catch (e) { error.value = e.message }
}

async function remove(it) {
  const q = it.dir ? `prefix=${encodeURIComponent(it.key)}` : `key=${encodeURIComponent(it.key)}`
  if (!confirm(it.dir ? `Hapus folder "${it.name}" beserta seluruh isinya?` : `Hapus "${it.name}"?`)) return
  try { await api('DELETE', `/api/buckets/${encodeURIComponent(bucket.value)}/object?${q}`); load(); loadInfo() } catch (e) { error.value = e.message }
}

function copyPath(it) {
  const url = `${cfg.value?.endpoint || ''}/${bucket.value}/${it.key.split('/').map(encodeURIComponent).join('/')}`
  navigator.clipboard?.writeText(url)
}
</script>

<template>
  <div class="page-head">
    <div>
      <h1>{{ bucket }} <span v-if="info" class="badge" :class="info.public ? 'soft' : 'gray'">{{ info.public ? 'publik' : 'privat' }}</span></h1>
      <p v-if="info">{{ info.objects }} objek · {{ fmtBytes(info.bytes) }} · <code>{{ cfg?.endpoint }}/{{ bucket }}</code></p>
    </div>
    <div class="actions">
      <input type="text" v-model="filter" placeholder="Saring nama…" style="width:180px" />
      <button class="btn" @click="showFolder = true">+ Folder</button>
      <button class="btn primary" @click="fileInput.click()">⬆ Unggah</button>
      <input ref="fileInput" type="file" multiple hidden @change="onPick" />
    </div>
  </div>
  <div v-if="error" class="alert error">{{ error }}</div>

  <div class="card" style="padding:0" @dragover.prevent @drop.prevent="onDrop">
    <div class="crumbs" style="padding:10px 12px;border-bottom:1px solid var(--border)">
      <a href="#" @click.prevent="go('')">{{ bucket }}</a>
      <template v-for="c in crumbs" :key="c.prefix"><span class="sep">/</span><a href="#" @click.prevent="go(c.prefix)">{{ c.name }}</a></template>
    </div>
    <table v-if="visible.length">
      <thead><tr><th>Nama</th><th>Tipe</th><th class="num">Ukuran</th><th>Diubah</th><th></th></tr></thead>
      <tbody>
        <tr v-for="it in visible" :key="it.key" class="row clickable" @click="open(it)">
          <td class="ellipsis"><span v-if="it.dir">📁</span><span v-else>📄</span> {{ it.name }}</td>
          <td class="muted">{{ it.dir ? 'folder' : it.contentType }}</td>
          <td class="num">{{ it.dir ? '' : fmtBytes(it.size) }}</td>
          <td class="muted nowrap">{{ it.dir ? '' : fmtDate(it.modified) }}</td>
          <td class="nowrap" style="text-align:right" @click.stop>
            <a v-if="!it.dir" class="btn sm ghost" :href="downloadUrl(bucket, it.key)">Unduh</a>
            <button v-if="!it.dir" class="btn sm ghost" title="Salin URL S3" @click="copyPath(it)">URL</button>
            <button class="btn sm ghost danger" @click="remove(it)">Hapus</button>
          </td>
        </tr>
      </tbody>
    </table>
    <div v-else-if="!loading" class="empty">Folder kosong. Seret berkas ke sini atau klik Unggah.</div>
    <div v-if="next" style="padding:10px;text-align:center"><button class="btn" :disabled="loading" @click="load(true)">Muat lebih banyak</button></div>
  </div>

  <div v-if="uploads.length" class="uploads card">
    <b>Unggahan</b>
    <div v-for="u in uploads" :key="u.name" class="item">
      <span class="ellipsis" style="display:inline-block;max-width:220px;vertical-align:bottom">{{ u.name }}</span>
      <span v-if="u.error" style="color:var(--danger)"> · {{ u.error }}</span><span v-else class="muted"> · {{ u.pct }}%</span>
      <div class="bar"><i :style="{ width: u.pct + '%' }"></i></div>
    </div>
  </div>

  <Modal v-if="showFolder" title="Folder baru" @close="showFolder = false">
    <form @submit.prevent="createFolder">
      <label class="field"><span>Nama folder di dalam <code>/{{ prefix }}</code></span><input type="text" v-model="folderName" required autofocus /></label>
    </form>
    <template #foot>
      <button class="btn" @click="showFolder = false">Batal</button>
      <button class="btn primary" @click="createFolder">Buat</button>
    </template>
  </Modal>
</template>
