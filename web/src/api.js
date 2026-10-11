import { reactive } from 'vue'

function load(key) {
  try { return localStorage.getItem(key) } catch { return null }
}
function save(key, v) {
  try { v == null ? localStorage.removeItem(key) : localStorage.setItem(key, v) } catch {}
}

export const auth = reactive({
  token: load('jasstore.token'),
  user: JSON.parse(load('jasstore.user') || 'null'),
  set(token, user) { this.token = token; this.user = user; save('jasstore.token', token); save('jasstore.user', JSON.stringify(user)) },
  clear() { this.token = null; this.user = null; save('jasstore.token', null); save('jasstore.user', null) },
})

export class ApiError extends Error {
  constructor(status, message) { super(message); this.status = status }
}

export async function api(method, path, body, opts = {}) {
  const headers = {}
  if (auth.token) headers.Authorization = 'Bearer ' + auth.token
  let payload = body
  if (body !== undefined && !(body instanceof Blob) && !(body instanceof File)) {
    headers['Content-Type'] = 'application/json'
    payload = JSON.stringify(body)
  } else if (body instanceof Blob && body.type) {
    headers['Content-Type'] = body.type
  }
  const res = await fetch(path, { method, headers, body: payload, signal: opts.signal })
  if (res.status === 401 && !path.endsWith('/api/login')) {
    auth.clear()
    location.href = '/login'
    throw new ApiError(401, 'sesi berakhir')
  }
  if (res.status === 204) return null
  const ct = res.headers.get('content-type') || ''
  const data = ct.includes('json') ? await res.json() : await res.text()
  if (!res.ok) throw new ApiError(res.status, data?.error || res.statusText)
  return data
}

export function downloadUrl(bucket, key, download = true) {
  const p = new URLSearchParams({ key, token: auth.token })
  if (download) p.set('download', '1')
  return `/api/buckets/${encodeURIComponent(bucket)}/object?${p}`
}

export function fmtBytes(n) {
  if (n == null) return '-'
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let v = Number(n)
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++ }
  return (i === 0 ? v : v.toFixed(v < 10 ? 2 : 1)) + ' ' + u[i]
}

export function fmtNum(n) {
  if (n == null) return '-'
  if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M'
  if (n >= 1e4) return (n / 1e3).toFixed(1) + 'K'
  return Number(n).toLocaleString('id-ID')
}

export function fmtDate(s) {
  if (!s) return '-'
  const d = new Date(s)
  if (d.getFullYear() < 1980) return '-'
  return d.toLocaleString('id-ID', { dateStyle: 'medium', timeStyle: 'short' })
}
