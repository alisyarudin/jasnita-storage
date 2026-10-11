<script setup>
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { auth } from './api.js'

const route = useRoute()
const router = useRouter()
const loggedIn = computed(() => !!auth.token && route.path !== '/login')

function logout() {
  auth.clear()
  router.push('/login')
}
</script>

<template>
  <div v-if="loggedIn" class="shell">
    <aside class="sidebar">
      <div class="brand">
        <svg viewBox="0 0 24 24" aria-hidden="true"><rect x="3" y="4" width="18" height="6" rx="1.5" fill="#2563eb"/><rect x="3" y="14" width="18" height="6" rx="1.5" fill="#60a5fa"/></svg>
        JasStore
      </div>
      <nav class="nav">
        <router-link to="/">Dashboard</router-link>
        <router-link to="/buckets" :class="{ 'router-link-active': route.path.startsWith('/buckets') }">Bucket</router-link>
        <router-link to="/keys">Access Key</router-link>
      </nav>
      <div class="spacer"></div>
      <div class="userbox">
        <b>{{ auth.user?.name || auth.user?.accessKey }}</b>
        <span>{{ auth.user?.admin ? 'Admin' : 'Pengguna' }} · <a href="#" @click.prevent="logout">Keluar</a></span>
      </div>
    </aside>
    <main class="main"><router-view /></main>
  </div>
  <router-view v-else />
</template>
