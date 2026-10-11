<script setup>
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, auth, downloadUrl, fmtBytes, fmtDate, previewKind } from '../api.js'
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
const preview = ref(null)      // item yang dipratinjau
const previewText = ref('')
const previewErr = ref('')
const previewKindOf = computed(() => preview.value ? previewKind(preview.value.name, preview.value.contentType) : 'none')
const previewSrc = computed(() => preview.value ? downloadUrl(bucket.value, preview.value.key, false) : '')
const files = computed(() => visible.value.filter(i => !i.dir))
const previewIdx = computed(() => files.value.findIndex(f => f.key === preview.value?.key))

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
function open(it) { it.dir ? go(it.key) : openPreview(it) }

async function openPreview(it) {
  preview.value = it
  previewText.value = ''
  previewErr.value = ''
  if (previewKind(it.name, it.contentType) === 'text') {
    if (it.size > 1024 * 1024) { previewErr.value = 'Berkas teks lebih dari 1 MB, unduh untuk melihat isinya.'; return }
    try {
      const res = await fetch(previewSrc.value)
      if (!res.ok) throw new Error('gagal memuat (' + res.status + ')')
      previewText.value = await res.text()
    } catch (e) { previewErr.value = e.message }
  }
}
function stepPreview(d) {
  const i = previewIdx.value + d
  if (i >= 0 && i < files.value.length) openPreview(files.value[i])
}
function onKey(e) {
  if (!preview.value) return
  if (e.key === 'Escape') preview.value = null
  if (e.key === 'ArrowRight') stepPreview(1)
  if (e.key === 'ArrowLeft') stepPreview(-1)
}
onMounted(() => window.addEventListener('keydown', onKey))
onUnmounted(() => window.removeEventListener('keydown', onKey))


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

  <div v-if="preview" class="modal-bg" @mousedown.self="preview = null">
    <div class="modal wide" role="dialog" :aria-label="preview.name">
      <div style="display:flex;align-items:center;justify-content:space-between;gap:12px;margin-bottom:12px">
        <h2 class="ellipsis" style="margin:0;max-width:60%">{{ preview.name }}</h2>
        <div class="preview-nav">
          <button class="btn sm" :disabled="previewIdx <= 0" @click="stepPreview(-1)" title="Sebelumnya (←)">←</button>
          <span class="muted" style="font-size:12px">{{ previewIdx + 1 }} / {{ files.length }}</span>
          <button class="btn sm" :disabled="previewIdx >= files.length - 1" @click="stepPreview(1)" title="Berikutnya (→)">→</button>
          <button class="btn sm ghost" @click="preview = null" title="Tutup (Esc)">✕</button>
        </div>
      </div>
      <div class="preview">
        <div class="stage">
          <audio v-if="previewKindOf === 'audio'" :key="preview.key" :src="previewSrc" controls preload="metadata" autoplay></audio>
          <video v-else-if="previewKindOf === 'video'" :key="preview.key" :src="previewSrc" controls preload="metadata"></video>
          <img v-else-if="previewKindOf === 'image'" :key="preview.key" :src="previewSrc" :alt="preview.name" />
          <iframe v-else-if="previewKindOf === 'pdf'" :key="preview.key" :src="previewSrc" :title="preview.name"></iframe>
          <template v-else-if="previewKindOf === 'text'">
            <div v-if="previewErr" class="none">{{ previewErr }}</div>
            <pre v-else>{{ previewText }}</pre>
          </template>
          <div v-else class="none"><b>📄</b>Tidak ada pratinjau untuk tipe ini.<br />Unduh untuk membuka di aplikasi lain.</div>
        </div>
        <dl>
          <dt>Ukuran</dt><dd>{{ fmtBytes(preview.size) }}</dd>
          <dt>Tipe</dt><dd>{{ preview.contentType || '-' }}</dd>
          <dt>Diubah</dt><dd>{{ fmtDate(preview.modified) }}</dd>
          <dt>ETag</dt><dd><code>{{ preview.etag }}</code></dd>
          <dt>Key</dt><dd><code>{{ preview.key }}</code></dd>
          <dt>URL S3</dt><dd><code style="font-size:11px">{{ cfg?.endpoint }}/{{ bucket }}/{{ preview.key }}</code></dd>
          <dd style="margin-top:14px;display:flex;gap:6px;flex-wrap:wrap">
            <a class="btn sm primary" :href="downloadUrl(bucket, preview.key)">⬇ Unduh</a>
            <a class="btn sm" :href="previewSrc" target="_blank" rel="noopener">Buka di tab baru</a>
            <button class="btn sm" @click="copyPath(preview)">Salin URL</button>
          </dd>
        </dl>
      </div>
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
