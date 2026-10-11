import { createRouter, createWebHistory } from 'vue-router'
import { auth } from './api.js'
import Login from './pages/Login.vue'
import Dashboard from './pages/Dashboard.vue'
import Buckets from './pages/Buckets.vue'
import BucketView from './pages/BucketView.vue'
import AccessKeys from './pages/AccessKeys.vue'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login', component: Login, meta: { public: true } },
    { path: '/', component: Dashboard },
    { path: '/buckets', component: Buckets },
    { path: '/buckets/:name', component: BucketView },
    { path: '/keys', component: AccessKeys },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

router.beforeEach((to) => {
  if (!to.meta.public && !auth.token) return { path: '/login', query: { next: to.fullPath } }
  if (to.meta.public && auth.token) return '/'
})

export default router
