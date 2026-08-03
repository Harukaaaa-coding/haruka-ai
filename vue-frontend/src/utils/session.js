const csrfCookieName = 'gopherai_csrf'

let authenticated = false
let sessionProbe = null

// Existing browser sessions used a JavaScript-readable long-lived token. Drop
// it during the migration so a successful release cannot leave a stealable
// credential behind in an old browser profile.
if (typeof window !== 'undefined') {
  try {
    window.localStorage.removeItem('token')
  } catch {
    // Storage can be disabled by browser privacy settings; cookie auth still
    // works without it.
  }
}

const decodeCookiePart = value => {
  try {
    return decodeURIComponent(value)
  } catch {
    return value
  }
}

export const readCookie = name => {
  if (typeof document === 'undefined') return ''
  const prefix = `${name}=`
  for (const part of document.cookie.split(';')) {
    const cookie = part.trim()
    if (cookie.startsWith(prefix)) {
      return decodeCookiePart(cookie.slice(prefix.length))
    }
  }
  return ''
}

// The CSRF cookie is intentionally readable by JavaScript. It contains no
// credential; it is paired with the HttpOnly session cookie by the backend.
export const csrfToken = () => readCookie(csrfCookieName)

export const markAuthenticated = () => {
  authenticated = true
}

export const clearAuthentication = () => {
  authenticated = false
}

export const isAuthenticated = () => authenticated

// Keep only one request in flight when several route guards run during app
// startup. The caller supplies the transport to keep this module independent
// from Axios and easy to reuse.
export const verifySession = request => {
  if (authenticated) return Promise.resolve(true)
  if (sessionProbe) return sessionProbe

  sessionProbe = Promise.resolve()
    .then(request)
    .then(response => {
      authenticated = Number(response?.data?.status_code) === 1000
      return authenticated
    })
    .catch(() => {
      authenticated = false
      return false
    })
    .finally(() => {
      sessionProbe = null
    })

  return sessionProbe
}
