import { mkdirSync, readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import puppeteer from 'puppeteer-core'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..')
const output = resolve(root, 'docs', 'screenshots')
const apiBase = (process.env.KNOWFLOW_API_URL || 'http://localhost:8080/api/v1').replace(/\/$/, '')
const webURL = process.env.KNOWFLOW_WEB_URL || 'http://localhost:5173'
const chromePath = process.env.CHROME_PATH || 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe'

async function api(path, options = {}, token = '') {
  const headers = new Headers(options.headers || {})
  if (token) headers.set('Authorization', `Bearer ${token}`)
  if (options.body && !(options.body instanceof FormData)) headers.set('Content-Type', 'application/json')
  const response = await fetch(`${apiBase}${path}`, { ...options, headers })
  const payload = await response.json()
  if (!response.ok) throw new Error(`${payload.error?.code || response.status}: ${payload.error?.message || 'request failed'}`)
  return payload.data
}

async function seed() {
  const email = `screenshot-${Date.now()}@knowflow.local`
  const pair = await api('/auth/register', { method: 'POST', body: JSON.stringify({ email, password: 'screenshot-demo-password' }) })
  const token = pair.access_token
  const kb = await api('/knowledge-bases', {
    method: 'POST',
    body: JSON.stringify({
      name: 'KnowFlow 产品与架构', description: '发布演示：检索、索引、问答与离线评测资料', embedding_model: 'fake-embedding',
      retrieval_config: { chunk_size: 800, chunk_overlap: 120, dense_top_k: 20, sparse_top_k: 20, rerank_top_k: 10, final_top_k: 5, minimum_score: 0, rrf_k: 60, rerank_enabled: true },
    }),
  }, token)
  const body = new FormData()
  body.append('file', new Blob([readFileSync(resolve(root, 'demo', 'knowflow-demo.md'))], { type: 'text/markdown' }), 'knowflow-demo.md')
  const upload = await api(`/knowledge-bases/${kb.id}/documents`, { method: 'POST', body }, token)
  let document = upload.document
  for (let attempt = 0; attempt < 120 && document.status !== 'ready'; attempt += 1) {
    await new Promise((done) => setTimeout(done, 500))
    document = await api(`/documents/${document.id}`, {}, token)
    if (document.status === 'failed') throw new Error(`indexing failed: ${document.error_code}`)
  }
  if (document.status !== 'ready') throw new Error('indexing timed out')
  const conversation = await api('/conversations', { method: 'POST', body: JSON.stringify({ knowledge_base_id: kb.id, title: '发布验收问答' }) }, token)
  const answer = await fetch(`${apiBase}/conversations/${conversation.id}/messages`, {
    method: 'POST', headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
    body: JSON.stringify({ content: 'KnowFlow 支持哪些文档格式？' }),
  })
  if (!answer.ok) throw new Error(`chat failed: ${answer.status}`)
  await answer.text()
  return pair
}

mkdirSync(output, { recursive: true })
const session = await seed()
const browser = await puppeteer.launch({ executablePath: chromePath, headless: true, args: ['--no-sandbox', '--disable-dev-shm-usage'], defaultViewport: { width: 1440, height: 960, deviceScaleFactor: 1 } })
try {
  const page = await browser.newPage()
  await page.goto(webURL, { waitUntil: 'networkidle0' })
  await page.screenshot({ path: resolve(output, '01-sign-in.png') })

  await page.evaluate((value) => localStorage.setItem('knowflow.session', JSON.stringify(value)), session)
  await page.reload({ waitUntil: 'networkidle0' })
  await page.waitForSelector('.kb-card')
  await page.screenshot({ path: resolve(output, '02-knowledge-bases.png') })

  await page.click('.kb-card')
  await page.waitForSelector('tbody .status.ready')
  await page.screenshot({ path: resolve(output, '03-documents.png') })

  await page.evaluate(() => [...document.querySelectorAll('.sidebar nav button')].find((button) => button.textContent.includes('知识问答'))?.click())
  await page.waitForSelector('.citations button')
  await page.click('.citations button')
  await page.waitForSelector('.citation-drawer')
  await page.screenshot({ path: resolve(output, '04-grounded-chat.png') })
  console.log(`Screenshots written to ${output}`)
} finally {
  await browser.close()
}
