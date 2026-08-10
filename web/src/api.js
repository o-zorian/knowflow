const runtimeBase = globalThis.window?.__KNOWFLOW_CONFIG__?.apiBaseUrl
const API_BASE = (runtimeBase || import.meta.env?.VITE_API_BASE_URL || 'http://localhost:8080/api/v1').replace(/\/$/, '')

export class APIError extends Error {
  constructor(message, code, status) {
    super(message)
    this.name = 'APIError'
    this.code = code
    this.status = status
  }
}

export function parseSSEBlock(block) {
  let event = 'message'
  const data = []
  for (const rawLine of block.split(/\r?\n/)) {
    if (rawLine.startsWith('event:')) event = rawLine.slice(6).trim()
    if (rawLine.startsWith('data:')) data.push(rawLine.slice(5).trimStart())
  }
  if (!data.length) return null
  return { event, data: JSON.parse(data.join('\n')) }
}

export async function consumeSSE(response, onEvent) {
  if (!response.ok) await throwResponse(response)
  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  while (true) {
    const { done, value } = await reader.read()
    buffer += decoder.decode(value || new Uint8Array(), { stream: !done })
    const blocks = buffer.split(/\r?\n\r?\n/)
    buffer = blocks.pop() || ''
    for (const block of blocks) {
      const parsed = parseSSEBlock(block)
      if (parsed) onEvent(parsed)
    }
    if (done) break
  }
  if (buffer.trim()) {
    const parsed = parseSSEBlock(buffer)
    if (parsed) onEvent(parsed)
  }
}

async function throwResponse(response) {
  let payload = {}
  try { payload = await response.json() } catch { /* response is not JSON */ }
  throw new APIError(payload.error?.message || `请求失败 (${response.status})`, payload.error?.code, response.status)
}

export function createClient(session, onSession, onExpired) {
  async function request(path, options = {}, retry = true) {
    const headers = new Headers(options.headers || {})
    if (session.value?.access_token) headers.set('Authorization', `Bearer ${session.value.access_token}`)
    if (options.body && !(options.body instanceof FormData)) headers.set('Content-Type', 'application/json')
    const response = await fetch(`${API_BASE}${path}`, { ...options, headers })
    if (response.status === 401 && retry && session.value?.refresh_token) {
      const refreshed = await fetch(`${API_BASE}/auth/refresh`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ refresh_token: session.value.refresh_token }),
      })
      if (refreshed.ok) {
        const payload = await refreshed.json()
        onSession(payload.data)
        return request(path, options, false)
      }
      onExpired()
    }
    if (!response.ok) await throwResponse(response)
    if (options.raw) return response
    return (await response.json()).data
  }

  return {
    request,
    auth(mode, email, password) {
      return request(`/auth/${mode}`, { method: 'POST', body: JSON.stringify({ email, password }) }, false)
    },
    upload(kbID, file) {
      const body = new FormData()
      body.append('file', file)
      return request(`/knowledge-bases/${kbID}/documents`, { method: 'POST', body })
    },
    async streamMessage(conversationID, content, onEvent, signal) {
      const response = await request(`/conversations/${conversationID}/messages`, {
        method: 'POST', body: JSON.stringify({ content }), raw: true, signal,
      })
      await consumeSSE(response, onEvent)
    },
  }
}

export { API_BASE }
