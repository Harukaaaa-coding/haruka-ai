import axios from 'axios'
import {
  clearAuthentication,
  csrfToken,
  markAuthenticated,
  verifySession
} from './session'

const configuredApiBase = (process.env.VUE_APP_API_BASE || '').trim()
const cookieSessionHeader = 'X-GopherAI-Session'
const csrfHeader = 'X-CSRF-Token'

const normalizeApiBase = (base) => {
  if (!base) return '/api/v1'
  if (base === '/') return ''
  const normalized = base.replace(/\/+$/, '')
  if (/^(https?:)?\/\//i.test(normalized) || normalized.startsWith('/')) {
    return normalized
  }
  return `/${normalized}`
}

export const API_BASE_URL = normalizeApiBase(configuredApiBase)

export const buildApiUrl = (path) => {
  const normalizedPath = path.startsWith('/') ? path : `/${path}`
  return `${API_BASE_URL}${normalizedPath}`
}

const loginUrl = () => {
  const base = new URL(process.env.BASE_URL || '/', window.location.origin)
  return new URL('login', base)
}

export const handleUnauthorized = () => {
  clearAuthentication()
  if (typeof window === 'undefined') return

  const login = loginUrl()
  if (window.location.pathname === login.pathname) return
  const currentPath = `${window.location.pathname}${window.location.search}${window.location.hash}`
  login.searchParams.set('redirect', currentPath)
  window.location.assign(login.toString())
}

export const isAuthFailure = (data) => {
  const statusCode = Number(data?.status_code)
  return statusCode === 2006 || statusCode === 2007
}

const isSafeMethod = method => ['get', 'head', 'options'].includes((method || 'get').toLowerCase())

const api = axios.create({
  baseURL: API_BASE_URL,
  // Regular JSON calls should fail predictably. SSE uses fetch with its own
  // streaming lifecycle and is not constrained by this timeout.
  timeout: 30_000,
  withCredentials: true,
  headers: {
    [cookieSessionHeader]: 'cookie'
  }
})

api.interceptors.request.use(
  config => {
    config.headers = config.headers || {}
    config.headers[cookieSessionHeader] = 'cookie'
    if (!isSafeMethod(config.method)) {
      const token = csrfToken()
      if (token) config.headers[csrfHeader] = token
    }
    return config
  },
  error => Promise.reject(error)
)

api.interceptors.response.use(
  response => {
    if (!response.config.skipAuthRedirect && isAuthFailure(response.data)) {
      handleUnauthorized()
    }
    return response
  },
  error => {
    if (!error.config?.skipAuthRedirect && error.response?.status === 401) {
      handleUnauthorized()
    }
    return Promise.reject(error)
  }
)

export const ensureAuthenticated = () => verifySession(() => api.get('/user/session', {
  skipAuthRedirect: true
}))

export const markSessionAuthenticated = markAuthenticated

export const logout = async () => {
  try {
    await api.post('/user/logout', {}, { skipAuthRedirect: true })
  } finally {
    clearAuthentication()
  }
}

export const csrfHeaders = () => {
  const token = csrfToken()
  return token ? { [csrfHeader]: token, [cookieSessionHeader]: 'cookie' } : { [cookieSessionHeader]: 'cookie' }
}

export default api
