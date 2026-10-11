<script setup>
import { ref, onMounted } from 'vue'
import { api, auth, fmtDate } from '../api.js'
import Modal from '../components/Modal.vue'

const keys = ref([])
const buckets = ref([])
const error = ref('')
const editing = ref(null) // {accessKey?, name, admin, policies}
const created = ref(null) // {accessKey, secretKey}
const busy = ref(false)
const formError = ref('')

async function load() {
  try {
    keys.value = await api('GET', '/api/keys')
    if (auth.user?.admin) buckets.value = await api('GET', '/api/buckets')
  } catch (e) { error.value = e.message }
}
onMounted(load)

function startCreate() { editing.value = { name: '', admin: false, policies: [{ bucket: '*', permission: 'readwrite' }] } }
function startEdit(k) { editing.value = { accessKey: k.accessKey, name: k.name, admin: k.admin, enabled: k.enabled, policies: k.policies.map(p => ({ ...p })) } }

async function save() {
  formError.value = ''
  busy.value = true
  const body = { name: editing.value.name, admin: editing.value.admin, policies: editing.value.admin ? [] : editing.value.policies }
  try {
    if (editing.value.accessKey) {
      await api('PATCH', `/api/keys/${editing.value.accessKey}`, body)
    } else {
      const r = await api('POST', '/api/keys', body)
      created.value = { accessKey: r.key.accessKey, secretKey: r.secretKey }
    }
    editing.value = null
    await load()
  } catch (e) { formError.value = e.message } finally { busy.value = false }
}

async function toggle(k) {
  try { await api('PATCH', `/api/keys/${k.accessKey}`, { enabled: !k.enabled }); await load() } catch (e) { error.value = e.message }
}
async function remove(k) {
  if (!confirm(`Hapus access key ${k.accessKey} (${k.name || 'tanpa nama'})? Aplikasi yang memakainya akan langsung ditolak.`)) return
  try { await api('DELETE', `/api/keys/${k.accessKey}`); await load() } catch (e) { error.value = e.message }
}
function policyText(k) {
  if (k.admin) return 'Admin: semua bucket, buat/hapus bucket, kelola key'
  if (!k.policies?.length) return 'Tanpa izin'
  return k.policies.map(p => `${p.bucket === '*' ? 'semua bucket' : p.bucket}: ${p.permission === 'readwrite' ? 'baca+tulis' : 'baca'}`).join(' · ')
}
function copy(t) { navigator.clipboard?.writeText(t) }
</script>

<template>
  <div class="page-head">
    <div><h1>Access Key</h1><p>Kredensial S3 untuk aplikasi (Laravel, aws cli). Secret key hanya terlihat sekali saat dibuat.</p></div>
    <div class="actions" v-if="auth.user?.admin"><button class="btn primary" @click="startCreate">+ Key baru</button></div>
  </div>
  <div v-if="error" class="alert error">{{ error }}</div>

  <div class="card" style="padding:0">
    <table>
      <thead><tr><th>Access key</th><th>Nama</th><th>Izin</th><th>Status</th><th>Dibuat</th><th></th></tr></thead>
      <tbody>
        <tr v-for="k in keys" :key="k.accessKey" class="row">
          <td><code>{{ k.accessKey }}</code></td>
          <td>{{ k.name || '-' }}</td>
          <td class="muted" style="max-width:360px">{{ policyText(k) }}</td>
          <td><span class="badge" :class="k.enabled ? 'ok' : 'warn'">{{ k.enabled ? 'aktif' : 'nonaktif' }}</span></td>
          <td class="muted nowrap">{{ fmtDate(k.createdAt) }}</td>
          <td class="nowrap" style="text-align:right" v-if="auth.user?.admin">
            <button class="btn sm ghost" @click="startEdit(k)">Ubah</button>
            <button class="btn sm ghost" @click="toggle(k)">{{ k.enabled ? 'Nonaktifkan' : 'Aktifkan' }}</button>
            <button class="btn sm ghost danger" @click="remove(k)">Hapus</button>
          </td>
          <td v-else></td>
        </tr>
      </tbody>
    </table>
  </div>

  <Modal v-if="editing" :title="editing.accessKey ? 'Ubah access key' : 'Access key baru'" @close="editing = null">
    <div v-if="formError" class="alert error">{{ formError }}</div>
    <label class="field"><span>Nama / keterangan</span><input type="text" v-model="editing.name" placeholder="mis. clicktocall-v3 produksi" autofocus /></label>
    <label class="check" style="margin-bottom:12px"><input type="checkbox" v-model="editing.admin" /> Admin (semua bucket, buat/hapus bucket, kelola access key)</label>
    <div v-if="!editing.admin">
      <div style="font-size:12.5px;color:var(--ink-2);margin-bottom:6px">Izin per bucket</div>
      <div v-for="(p, i) in editing.policies" :key="i" style="display:flex;gap:8px;margin-bottom:8px;align-items:center">
        <select v-model="p.bucket" style="flex:1">
          <option value="*">* (semua bucket, boleh membuat bucket bila baca+tulis)</option>
          <option v-for="b in buckets" :key="b.name" :value="b.name">{{ b.name }}</option>
        </select>
        <select v-model="p.permission" style="width:140px">
          <option value="read">baca</option>
          <option value="readwrite">baca + tulis</option>
        </select>
        <button class="btn sm ghost danger" @click="editing.policies.splice(i, 1)">✕</button>
      </div>
      <button class="btn sm" @click="editing.policies.push({ bucket: buckets[0]?.name || '*', permission: 'read' })">+ Tambah izin</button>
    </div>
    <template #foot>
      <button class="btn" @click="editing = null">Batal</button>
      <button class="btn primary" :disabled="busy" @click="save">Simpan</button>
    </template>
  </Modal>

  <Modal v-if="created" title="Access key dibuat" @close="created = null">
    <div class="alert ok">Simpan secret key sekarang. Setelah jendela ini ditutup, secret tidak bisa dilihat lagi.</div>
    <label class="field"><span>Access key</span><input type="text" :value="created.accessKey" readonly @focus="$event.target.select()" /></label>
    <label class="field"><span>Secret key</span><input type="text" :value="created.secretKey" readonly @focus="$event.target.select()" /></label>
    <template #foot>
      <button class="btn" @click="copy(`AWS_ACCESS_KEY_ID=${created.accessKey}\nAWS_SECRET_ACCESS_KEY=${created.secretKey}`)">Salin untuk .env</button>
      <button class="btn primary" @click="created = null">Selesai</button>
    </template>
  </Modal>
</template>
