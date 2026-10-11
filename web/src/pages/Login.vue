<script setup>
import { ref } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { api, auth } from '../api.js'

const router = useRouter()
const route = useRoute()
const accessKey = ref('')
const secretKey = ref('')
const error = ref('')
const busy = ref(false)

async function submit() {
  error.value = ''
  busy.value = true
  try {
    const r = await api('POST', '/api/login', { AccessKey: accessKey.value.trim(), SecretKey: secretKey.value })
    auth.set(r.token, r.user)
    router.push(route.query.next || '/')
  } catch (e) {
    error.value = e.message
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="login">
    <form class="card" @submit.prevent="submit">
      <div class="brand" style="padding-left:0">
        <svg viewBox="0 0 24 24" width="26" height="26" aria-hidden="true"><rect x="3" y="4" width="18" height="6" rx="1.5" fill="#2563eb"/><rect x="3" y="14" width="18" height="6" rx="1.5" fill="#60a5fa"/></svg>
        JasStore Console
      </div>
      <div v-if="error" class="alert error">{{ error }}</div>
      <label class="field"><span>Access key</span><input type="text" v-model="accessKey" autocomplete="username" required autofocus /></label>
      <label class="field"><span>Secret key</span><input type="password" v-model="secretKey" autocomplete="current-password" required /></label>
      <button class="btn primary" style="width:100%;justify-content:center" :disabled="busy">Masuk</button>
    </form>
  </div>
</template>
