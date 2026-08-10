<script setup>
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { API_BASE, createClient } from './api.js'

const saved = localStorage.getItem('knowflow.session')
const session = ref(saved ? JSON.parse(saved) : null)
const authMode = ref('login')
const authForm = ref({ email: '', password: '' })
const authBusy = ref(false)
const activeView = ref('knowledge')
const notice = ref(null)
const knowledgeBases = ref([])
const selectedKB = ref(null)
const documents = ref([])
const chunks = ref([])
const chunkDocument = ref(null)
const uploadBusy = ref(false)
const conversations = ref([])
const selectedConversation = ref(null)
const messages = ref([])
const question = ref('')
const streaming = ref(false)
const citationFocus = ref(null)
const adminSummary = ref(null)
const adminJobs = ref([])
const adminUsage = ref([])
let documentTimer = null
let streamController = null

function setSession(value) {
  session.value = value
  localStorage.setItem('knowflow.session', JSON.stringify(value))
}
function clearSession() {
  session.value = null
  localStorage.removeItem('knowflow.session')
  selectedKB.value = null
  selectedConversation.value = null
}
const api = createClient(session, setSession, clearSession)
const user = computed(() => session.value?.user)
const selectedReady = computed(() => documents.value.some((doc) => doc.status === 'ready'))

function flash(message, kind = 'success') {
  notice.value = { message, kind }
  window.setTimeout(() => { if (notice.value?.message === message) notice.value = null }, 4500)
}
function errorMessage(error) {
  flash(error?.message || '操作失败，请稍后重试', 'error')
}
function formatDate(value) {
  return value ? new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value)) : '—'
}
function formatBytes(value) {
  if (value < 1024) return `${value} B`
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`
  return `${(value / 1024 / 1024).toFixed(1)} MB`
}

async function authenticate() {
  authBusy.value = true
  try {
    const result = await api.auth(authMode.value, authForm.value.email, authForm.value.password)
    setSession(result)
    await loadKnowledgeBases()
    flash(authMode.value === 'register' ? '注册成功，欢迎来到 KnowFlow' : '欢迎回来')
  } catch (error) { errorMessage(error) } finally { authBusy.value = false }
}
async function logout() {
  const refreshToken = session.value?.refresh_token
  try {
    if (refreshToken) await api.request('/auth/logout', { method: 'POST', body: JSON.stringify({ refresh_token: refreshToken }) })
  } catch { /* local logout still proceeds */ }
  clearSession()
}

async function loadKnowledgeBases() {
  try {
    const result = await api.request('/knowledge-bases?page_size=100')
    knowledgeBases.value = result.items
    if (selectedKB.value) selectedKB.value = result.items.find((item) => item.id === selectedKB.value.id) || null
  } catch (error) { errorMessage(error) }
}
async function createKnowledgeBase(event) {
  const form = new FormData(event.currentTarget)
  try {
    const created = await api.request('/knowledge-bases', {
      method: 'POST',
      body: JSON.stringify({
        name: form.get('name'), description: form.get('description'), embedding_model: form.get('embedding_model'),
        retrieval_config: {
          chunk_size: 800, chunk_overlap: 120,
          dense_top_k: Number(form.get('dense_top_k')), sparse_top_k: Number(form.get('sparse_top_k')),
          rerank_top_k: 10, final_top_k: 5, minimum_score: 0, rrf_k: 60,
          rerank_enabled: form.get('rerank_enabled') === 'on',
        },
      }),
    })
    event.currentTarget.reset()
    await loadKnowledgeBases()
    await selectKnowledgeBase(created)
    flash('知识库创建成功')
  } catch (error) { errorMessage(error) }
}
async function selectKnowledgeBase(kb) {
  selectedKB.value = kb
  activeView.value = 'documents'
  await loadDocuments()
}
async function loadDocuments() {
  if (!selectedKB.value) return
  try {
    const result = await api.request(`/knowledge-bases/${selectedKB.value.id}/documents?page_size=100`)
    documents.value = result.items
    const processing = result.items.some((doc) => !['ready', 'failed'].includes(doc.status))
    if (processing && !documentTimer) documentTimer = window.setInterval(loadDocuments, 1800)
    if (!processing && documentTimer) { clearInterval(documentTimer); documentTimer = null }
    await loadKnowledgeBases()
  } catch (error) { errorMessage(error) }
}
async function uploadDocument(event) {
  const file = event.target.files?.[0]
  if (!file || !selectedKB.value) return
  uploadBusy.value = true
  try {
    const result = await api.upload(selectedKB.value.id, file)
    flash(result.duplicate ? '相同文件已存在，未重复索引' : '上传成功，索引任务已进入队列')
    await loadDocuments()
  } catch (error) { errorMessage(error) } finally {
    uploadBusy.value = false
    event.target.value = ''
  }
}
async function retryDocument(doc) {
  try { await api.request(`/documents/${doc.id}/retry`, { method: 'POST' }); await loadDocuments(); flash('已重新加入索引队列') }
  catch (error) { errorMessage(error) }
}
async function previewChunks(doc) {
  try {
    const result = await api.request(`/documents/${doc.id}/chunks?page_size=50`)
    chunks.value = result.items
    chunkDocument.value = doc
  } catch (error) { errorMessage(error) }
}

async function openChat() {
  activeView.value = 'chat'
  try {
    const result = await api.request('/conversations?page_size=100')
    conversations.value = result.items.filter((item) => !selectedKB.value || item.knowledge_base_id === selectedKB.value.id)
    if (conversations.value.length && !selectedConversation.value) await selectConversation(conversations.value[0])
  } catch (error) { errorMessage(error) }
}
async function createConversation() {
  if (!selectedKB.value) return flash('请先选择一个知识库', 'error')
  try {
    const created = await api.request('/conversations', {
      method: 'POST', body: JSON.stringify({ knowledge_base_id: selectedKB.value.id, title: `${selectedKB.value.name} 问答` }),
    })
    conversations.value.unshift(created)
    await selectConversation(created)
    flash('新对话已创建')
  } catch (error) { errorMessage(error) }
}
async function selectConversation(conversation) {
  try {
    const detail = await api.request(`/conversations/${conversation.id}`)
    selectedConversation.value = detail.conversation
    messages.value = detail.messages
  } catch (error) { errorMessage(error) }
}
async function sendQuestion() {
  const content = question.value.trim()
  if (!content || streaming.value) return
  if (!selectedConversation.value) await createConversation()
  if (!selectedConversation.value) return
  question.value = ''
  streaming.value = true
  citationFocus.value = null
  messages.value.push({ id: `local-user-${Date.now()}`, role: 'user', content, status: 'completed', citations: [] })
  const assistant = { id: `local-assistant-${Date.now()}`, role: 'assistant', content: '', status: 'streaming', citations: [], retrieval_trace: {} }
  messages.value.push(assistant)
  await nextTick()
  streamController = new AbortController()
  try {
    await api.streamMessage(selectedConversation.value.id, content, ({ event, data }) => {
      if (event === 'message.started') assistant.id = data.message_id || assistant.id
      if (event === 'retrieval.completed') assistant.retrieval_trace = data
      if (event === 'message.delta') assistant.content += data.content || data.delta || ''
      if (event === 'citation') assistant.citations.push(data)
      if (event === 'usage') Object.assign(assistant, data)
      if (event === 'message.completed') assistant.status = 'completed'
      if (event === 'error') { assistant.status = 'failed'; assistant.error_code = data.code; errorMessage(new Error(data.message)) }
    }, streamController.signal)
    await selectConversation(selectedConversation.value)
  } catch (error) {
    if (error.name !== 'AbortError') { assistant.status = 'failed'; errorMessage(error) }
  } finally { streaming.value = false; streamController = null }
}

async function loadAdmin() {
  activeView.value = 'admin'
  try {
    const [summary, jobs, usage] = await Promise.all([
      api.request('/admin/metrics/summary'),
      api.request('/admin/ingestion-jobs?page_size=20'),
      api.request('/admin/model-usage?page_size=20'),
    ])
    adminSummary.value = summary
    adminJobs.value = jobs.items
    adminUsage.value = usage.items
  } catch (error) { errorMessage(error) }
}

watch(session, (value) => { if (value) loadKnowledgeBases() }, { immediate: true })
onBeforeUnmount(() => { if (documentTimer) clearInterval(documentTimer); streamController?.abort() })
</script>

<template>
  <div v-if="!session" class="auth-page">
    <section class="auth-story">
      <a class="brand light" href="#"><span class="brand-mark">K</span> KnowFlow</a>
      <div>
        <p class="eyebrow">ENTERPRISE KNOWLEDGE, GROUNDED</p>
        <h1>把分散的文档，<br><em>变成可靠的答案。</em></h1>
        <p class="hero-copy">从异步索引、混合检索到带原文引用的流式问答，KnowFlow 让团队知识真正流动起来。</p>
        <div class="feature-row"><span>01 · 多格式解析</span><span>02 · 混合检索</span><span>03 · 可验证引用</span></div>
      </div>
      <p class="auth-foot">Go · PostgreSQL + pgvector · Redis · MinIO</p>
    </section>
    <section class="auth-panel">
      <form class="auth-card" @submit.prevent="authenticate">
        <p class="kicker">{{ authMode === 'login' ? '欢迎回来' : '开始构建知识库' }}</p>
        <h2>{{ authMode === 'login' ? '登录 KnowFlow' : '创建账户' }}</h2>
        <p class="muted">{{ authMode === 'login' ? '继续探索你的团队知识。' : '注册后即可创建并索引第一个知识库。' }}</p>
        <label>邮箱<input v-model="authForm.email" type="email" autocomplete="email" required placeholder="you@company.com"></label>
        <label>密码<input v-model="authForm.password" type="password" minlength="8" autocomplete="current-password" required placeholder="至少 8 个字符"></label>
        <button class="primary full" :disabled="authBusy">{{ authBusy ? '请稍候…' : (authMode === 'login' ? '登录' : '注册并登录') }}</button>
        <p class="switch">{{ authMode === 'login' ? '还没有账户？' : '已经有账户？' }} <button type="button" class="link" @click="authMode = authMode === 'login' ? 'register' : 'login'">{{ authMode === 'login' ? '立即注册' : '返回登录' }}</button></p>
        <small>开发环境默认使用离线 Fake 模型，不会产生外部调用费用。</small>
      </form>
    </section>
    <div v-if="notice" class="toast" :class="notice.kind">{{ notice.message }}</div>
  </div>

  <div v-else class="app-shell">
    <aside class="sidebar">
      <a class="brand" href="#"><span class="brand-mark">K</span> KnowFlow</a>
      <nav>
        <button :class="{ active: activeView === 'knowledge' }" @click="activeView = 'knowledge'; loadKnowledgeBases()"><span>◇</span> 知识库</button>
        <button :class="{ active: activeView === 'documents' }" :disabled="!selectedKB" @click="activeView = 'documents'; loadDocuments()"><span>▤</span> 文档索引</button>
        <button :class="{ active: activeView === 'chat' }" @click="openChat"><span>◌</span> 知识问答</button>
        <button v-if="user.role === 'admin'" :class="{ active: activeView === 'admin' }" @click="loadAdmin"><span>⌁</span> 系统治理</button>
      </nav>
      <div class="sidebar-context" v-if="selectedKB"><small>当前知识库</small><strong>{{ selectedKB.name }}</strong><span>{{ selectedKB.ready_chunk_count }} 个可用分块</span></div>
      <div class="profile"><div class="avatar">{{ user.email[0].toUpperCase() }}</div><div><strong>{{ user.email }}</strong><small>{{ user.role }}</small></div><button title="退出" @click="logout">↗</button></div>
    </aside>

    <main class="workspace">
      <header class="topbar"><div><p class="breadcrumb">KNOWFLOW / {{ activeView.toUpperCase() }}</p><h1>{{ { knowledge: '知识库', documents: '文档索引', chat: '知识问答', admin: '系统治理' }[activeView] }}</h1></div><span class="api-state"><i></i> API 已连接 · {{ API_BASE }}</span></header>

      <section v-if="activeView === 'knowledge'" class="content-grid">
        <div class="main-column">
          <div class="section-head"><div><p class="kicker">YOUR KNOWLEDGE</p><h2>让每份资料都可被检索</h2></div><span class="count">{{ knowledgeBases.length }} 个知识库</span></div>
          <div v-if="knowledgeBases.length" class="kb-grid">
            <button v-for="kb in knowledgeBases" :key="kb.id" class="kb-card" @click="selectKnowledgeBase(kb)">
              <span class="kb-icon">{{ kb.name.slice(0, 1).toUpperCase() }}</span><span class="arrow">↗</span>
              <strong>{{ kb.name }}</strong><p>{{ kb.description || '暂无描述' }}</p>
              <span class="kb-stats"><b>{{ kb.document_count }}</b> 文档 <b>{{ kb.ready_chunk_count }}</b> 分块</span>
            </button>
          </div>
          <div v-else class="empty"><span>◇</span><h3>创建你的第一个知识库</h3><p>上传文档后，Worker 会自动解析、分块并建立索引。</p></div>
        </div>
        <form class="side-card" @submit.prevent="createKnowledgeBase">
          <p class="kicker">NEW SPACE</p><h3>创建知识库</h3>
          <label>名称<input name="name" maxlength="120" required placeholder="例如：产品手册"></label>
          <label>描述<textarea name="description" rows="3" placeholder="这个知识库包含什么？"></textarea></label>
          <label>Embedding 模型<input name="embedding_model" required value="fake-embedding"></label>
          <div class="two"><label>Dense Top K<input name="dense_top_k" type="number" min="0" value="20"></label><label>Sparse Top K<input name="sparse_top_k" type="number" min="0" value="20"></label></div>
          <label class="check"><input name="rerank_enabled" type="checkbox"> 启用 Reranker</label>
          <button class="primary full">创建知识库</button>
        </form>
      </section>

      <section v-else-if="activeView === 'documents'" class="wide-section">
        <div class="section-head"><div><p class="kicker">ASYNC INGESTION</p><h2>{{ selectedKB?.name }}</h2><p class="muted">支持 PDF、DOCX、Markdown 与 TXT，单文件默认不超过 30 MB。</p></div><label class="upload-button" :class="{ disabled: uploadBusy }">{{ uploadBusy ? '上传中…' : '＋ 上传文档' }}<input type="file" accept=".pdf,.docx,.md,.markdown,.txt" :disabled="uploadBusy" @change="uploadDocument"></label></div>
        <div class="table-card">
          <table><thead><tr><th>文档</th><th>状态</th><th>进度</th><th>分块</th><th>更新时间</th><th></th></tr></thead>
            <tbody><tr v-for="doc in documents" :key="doc.id"><td><strong>{{ doc.filename }}</strong><small>{{ formatBytes(doc.size_bytes) }}</small></td><td><span class="status" :class="doc.status">{{ doc.status }}</span><small v-if="doc.error_message" class="danger">{{ doc.error_message }}</small></td><td><div class="progress"><i :style="{ width: `${doc.job?.progress ?? (doc.status === 'ready' ? 100 : 0)}%` }"></i></div><small>{{ doc.job?.stage || doc.status }} · {{ doc.job?.attempts || 0 }} 次尝试</small></td><td>{{ doc.chunk_count }}</td><td>{{ formatDate(doc.updated_at) }}</td><td class="actions"><button v-if="doc.status === 'ready'" @click="previewChunks(doc)">预览分块</button><button v-if="doc.status === 'failed'" @click="retryDocument(doc)">重试</button></td></tr>
            <tr v-if="!documents.length"><td colspan="6"><div class="empty compact"><span>▤</span><h3>还没有文档</h3><p>上传一个演示文档，观察完整异步索引过程。</p></div></td></tr></tbody>
          </table>
        </div>
        <div v-if="selectedReady" class="next-step"><span>索引已就绪</span><p>现在可以基于真实分块发起带引用的问答。</p><button class="primary" @click="openChat">开始问答 →</button></div>
      </section>

      <section v-else-if="activeView === 'chat'" class="chat-layout">
        <aside class="conversation-list"><button class="primary full" :disabled="!selectedKB" @click="createConversation">＋ 新建对话</button><button v-for="item in conversations" :key="item.id" :class="{ active: selectedConversation?.id === item.id }" @click="selectConversation(item)"><strong>{{ item.title }}</strong><small>{{ formatDate(item.updated_at) }}</small></button><p v-if="!selectedKB" class="muted">请先从知识库页面选择知识库。</p></aside>
        <div class="chat-panel">
          <div v-if="!selectedConversation" class="empty chat-empty"><span>◌</span><h2>用知识回答，而不是猜测</h2><p>创建对话后，回答会通过 SSE 实时呈现，并附带可核验的原文引用。</p></div>
          <template v-else>
            <div class="chat-title"><div><small>{{ selectedKB?.name }}</small><h2>{{ selectedConversation.title }}</h2></div><span class="grounded">GROUNDING ON</span></div>
            <div class="messages">
              <article v-for="message in messages" :key="message.id" class="message" :class="message.role">
                <div class="message-role">{{ message.role === 'user' ? 'YOU' : 'K' }}</div><div class="bubble"><p>{{ message.content }}<span v-if="message.status === 'streaming'" class="cursor"></span></p><div v-if="message.citations?.length" class="citations"><button v-for="citation in message.citations" :key="citation.number" @click="citationFocus = citation">[{{ citation.number }}] {{ citation.filename }} · {{ citation.location }}</button></div><small v-if="message.role === 'assistant' && message.status === 'completed'">{{ message.total_tokens || 0 }} tokens · {{ message.latency_ms || 0 }} ms · ${{ (message.estimated_cost_usd || 0).toFixed(6) }}</small><small v-if="message.status === 'failed'" class="danger">回答失败 · {{ message.error_code }}</small></div>
              </article>
            </div>
            <form class="composer" @submit.prevent="sendQuestion"><textarea v-model="question" rows="2" placeholder="询问当前知识库中的内容…" :disabled="streaming" @keydown.enter.exact.prevent="sendQuestion"></textarea><button class="send" :disabled="streaming || !question.trim()">{{ streaming ? '生成中' : '发送 ↑' }}</button><small>回答仅依据检索到的知识库证据生成</small></form>
          </template>
        </div>
      </section>

      <section v-else-if="activeView === 'admin'" class="wide-section">
        <div class="section-head"><div><p class="kicker">OBSERVABILITY</p><h2>服务与模型治理</h2></div><a class="outline" href="http://localhost:8080/metrics" target="_blank">Prometheus 指标 ↗</a></div>
        <div v-if="adminSummary" class="metric-grid"><div v-for="(value, key) in adminSummary" :key="key"><small>{{ key.replaceAll('_', ' ') }}</small><strong>{{ typeof value === 'number' ? Math.round(value * 10000) / 10000 : value }}</strong></div></div>
        <div class="admin-grid"><div class="table-card"><h3>失败索引任务</h3><table><thead><tr><th>文件</th><th>阶段</th><th>错误</th></tr></thead><tbody><tr v-for="job in adminJobs" :key="job.id"><td>{{ job.filename }}</td><td>{{ job.stage }}</td><td>{{ job.error_code || '—' }}</td></tr><tr v-if="!adminJobs.length"><td colspan="3">暂无失败任务</td></tr></tbody></table></div><div class="table-card"><h3>最近模型调用</h3><table><thead><tr><th>类型 / 模型</th><th>Token</th><th>延迟</th></tr></thead><tbody><tr v-for="usage in adminUsage" :key="usage.id"><td>{{ usage.request_type }} / {{ usage.model }}</td><td>{{ usage.prompt_tokens + usage.completion_tokens }}</td><td>{{ usage.latency_ms }} ms</td></tr><tr v-if="!adminUsage.length"><td colspan="3">暂无调用记录</td></tr></tbody></table></div></div>
      </section>
    </main>

    <div v-if="chunkDocument" class="drawer-backdrop" @click.self="chunkDocument = null"><aside class="drawer"><button class="close" @click="chunkDocument = null">×</button><p class="kicker">CHUNK PREVIEW</p><h2>{{ chunkDocument.filename }}</h2><article v-for="chunk in chunks" :key="chunk.id" class="chunk"><small>#{{ chunk.chunk_index + 1 }} · {{ chunk.heading_path || `段落 ${chunk.metadata?.paragraph_start ?? '—'}` }} · {{ chunk.token_count }} tokens</small><p>{{ chunk.content }}</p></article></aside></div>
    <div v-if="citationFocus" class="drawer-backdrop" @click.self="citationFocus = null"><aside class="drawer citation-drawer"><button class="close" @click="citationFocus = null">×</button><p class="kicker">SOURCE [{{ citationFocus.number }}]</p><h2>{{ citationFocus.filename }}</h2><div class="source-meta"><span>{{ citationFocus.location }}</span><span>score {{ citationFocus.score.toFixed(4) }}</span><span>chunk {{ citationFocus.chunk_index + 1 }}</span></div><blockquote>{{ citationFocus.excerpt }}</blockquote><small>文档 ID：{{ citationFocus.document_id }}<br>分块 ID：{{ citationFocus.chunk_id }}</small></aside></div>
    <div v-if="notice" class="toast" :class="notice.kind">{{ notice.message }}</div>
  </div>
</template>
