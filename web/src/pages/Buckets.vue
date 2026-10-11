<script setup>
import { ref, onMounted } from 'vue'
import { api, fmtBytes, fmtNum, fmtDate, auth } from '../api.js'
import Modal from '../components/Modal.vue'

const buckets = ref([])
const error = ref('')
const showCreate = ref(false)
const form = ref({ name: '', public: false })
const formError = ref('')
const busy = ref(false)

async function load() {
  try { buckets.value = await api('GET', '/api/buckets') } catch (e) { error.value = e.message }
}
onMounted(load)

async function create() {
  formError.value = ''
  busy.value = true
  try {
    await api('POST', '/api/buckets', form.value)
    showCreate.value = false
    form.value = { name: '', public: false }
    await load()
  } catch (e) { formError.value = e.message } finally { busy.value = false }
}

async function togglePublic(b) {
  try { await api('PATCH', `/api/buckets/${b.name}`, { public: !b.public }); await load() } catch (e) { error.value = e.message }
}

async function remove(b) {
  const force = b.objects > 0
  const msg = force
    ? `Bucket "${b.name}" berisi ${b.objects} objek (${fmtBytes(b.bytes)}). Hapus bucket BESERTA SELURUH ISINYA? Tidak bisa dibatalkan.`
    : `Hapus bucket kosong "${b.name}"?`
  if (!confirm(msg)) return
  if (force && prompt(`Ketik nama bucket untuk konfirmasi:`) !== b.name) return
  try { await api('DELETE', `/api/buckets/${b.name}` + (force ? '?force=1' : '')); await load() } catch (e) { error.value = e.message }
}
</script>

<template>
  <div class="page-head">
    <div><h1>Bucket</h1><p>Wadah objek. Nama bucket mengikuti aturan S3: 3-63 karakter, huruf kecil, angka, titik, strip.</p></div>
    <div class="actions"><button class="btn primary" @click="showCreate = true">+ Bucket baru</button></div>
  </div>
  <div v-if="error" class="alert error">{{ error }}</div>
  <div class="card" style="padding:0">
    <table v-if="buckets.length">
      <thead><tr><th>Nama</th><th>Akses</th><th class="num">Objek</th><th class="num">Ukuran</th><th>Dibuat</th><th></th></tr></thead>
      <tbody>
        <tr v-for="b in buckets" :key="b.name" class="row">
          <td><router-link :to="'/buckets/' + b.name"><b>{{ b.name }}</b></router-link></td>
          <td><span class="badge" :class="b.public ? 'soft' : 'gray'">{{ b.public ? 'publik (baca anonim)' : 'privat' }}</span></td>
          <td class="num">{{ fmtNum(b.objects) }}</td>
          <td class="num">{{ fmtBytes(b.bytes) }}</td>
          <td class="muted nowrap">{{ fmtDate(b.createdAt) }}</td>
          <td class="nowrap" style="text-align:right">
            <button class="btn sm ghost" @click="togglePublic(b)">{{ b.public ? 'Jadikan privat' : 'Jadikan publik' }}</button>
            <button v-if="auth.user?.admin || b.objects === 0" class="btn sm ghost danger" @click="remove(b)">Hapus</button>
          </td>
        </tr>
      </tbody>
    </table>
    <div v-else class="empty">Belum ada bucket.</div>
  </div>

  <Modal v-if="showCreate" title="Bucket baru" @close="showCreate = false">
    <form @submit.prevent="create">
      <div v-if="formError" class="alert error">{{ formError }}</div>
      <label class="field"><span>Nama bucket</span><input type="text" v-model="form.name" placeholder="mis. rekaman-call" pattern="[a-z0-9.-]{3,63}" required autofocus /></label>
      <label class="check"><input type="checkbox" v-model="form.public" /> Publik: objek bisa diunduh siapa pun lewat URL tanpa tanda tangan (listing tetap privat)</label>
    </form>
    <template #foot>
      <button class="btn" @click="showCreate = false">Batal</button>
      <button class="btn primary" :disabled="busy" @click="create">Buat</button>
    </template>
  </Modal>
</template>
