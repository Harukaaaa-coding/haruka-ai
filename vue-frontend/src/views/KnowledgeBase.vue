<template>
  <main class="knowledge-page" :aria-busy="loading">
    <header class="page-header">
      <button type="button" class="back-btn" @click="$router.push('/menu')">← 返回</button>
      <div>
        <h1>Knowledge Base 2.0</h1>
        <p>管理多知识库、多文档和可追溯的检索引用</p>
      </div>
      <button type="button" class="primary-btn" @click="createKnowledgeBase">＋ 新建知识库</button>
    </header>

    <section class="workspace">
      <aside class="library-panel">
        <div class="panel-title">
          <span>我的知识库</span>
          <button type="button" class="icon-btn" title="刷新" @click="loadKnowledgeBases">↻</button>
        </div>
        <div v-if="listError" class="list-error" role="alert">
          <span>{{ listError }}</span>
          <button type="button" class="text-btn" @click="loadKnowledgeBases">重试</button>
        </div>
        <div v-else-if="!knowledgeBases.length && !loading" class="empty-small">还没有知识库</div>
        <button
          v-for="base in knowledgeBases"
          :key="base.id"
          type="button"
          :class="['library-item', { active: selectedBase?.id === base.id }]"
          @click="selectKnowledgeBase(base)"
        >
          <span class="library-name">{{ base.name }}</span>
          <span class="library-meta">{{ base.document_count || 0 }} 个文档 · {{ base.chunk_count || 0 }} 个切块</span>
        </button>
      </aside>

      <section v-if="selectedBase" class="detail-panel">
        <div class="detail-header">
          <div>
            <h2>{{ selectedBase.name }}</h2>
            <p>{{ selectedBase.description || '暂无描述' }}</p>
          </div>
          <div class="header-actions">
            <button type="button" class="upload-btn" :disabled="uploading" @click="fileInput?.click()">
              {{ uploading ? '上传中…' : '上传文档' }}
            </button>
            <input ref="fileInput" type="file" accept=".md,.txt,text/markdown,text/plain" hidden @change="uploadDocument" />
            <button type="button" class="danger-btn" @click="deleteKnowledgeBase">删除知识库</button>
          </div>
        </div>

        <div v-if="detailLoading" class="detail-status" role="status">正在加载知识库详情…</div>
        <div v-else-if="detailError" class="detail-error" role="alert">
          <span>{{ detailError }}</span>
          <button type="button" class="text-btn" @click="retrySelectedKnowledgeBase">重试</button>
        </div>

        <div class="stats-row">
          <div class="stat-card"><strong>{{ documents.length }}</strong><span>文档</span></div>
          <div class="stat-card"><strong>{{ totalChunks }}</strong><span>切块</span></div>
          <div class="stat-card"><strong>{{ readyDocuments }}</strong><span>已就绪</span></div>
        </div>

        <section class="documents-section">
          <h3>文档与索引状态</h3>
          <div v-if="!documents.length" class="empty-box">
            <span aria-hidden="true">▤</span>
            <p>上传第一个 Markdown 或文本文件开始构建知识库</p>
          </div>
          <article v-for="document in documents" :key="document.id" class="document-row">
            <div class="document-main">
              <strong>{{ document.name }}</strong>
              <span>{{ formatBytes(document.size) }} · {{ formatDate(document.created_at) }}</span>
            </div>
            <span :class="['status-pill', `status-${document.status}`]">{{ statusLabel(document.status) }}</span>
            <span class="chunk-count">{{ document.chunk_count || 0 }} chunks</span>
            <button type="button" class="icon-btn danger-text" title="删除文档" @click="deleteDocument(document)">删除</button>
            <p v-if="document.error" class="document-error">{{ document.error }}</p>
          </article>
        </section>

        <section class="search-section">
          <div class="search-title">
            <div>
              <h3>检索调试</h3>
              <p>直接查看向量检索命中的原始切块和引用位置</p>
            </div>
          </div>
          <form class="search-form" @submit.prevent="searchKnowledgeBase">
            <input v-model="searchQuery" type="search" placeholder="输入问题，例如：文档中的关键结论是什么？" aria-label="知识库检索问题" />
            <button type="submit" class="primary-btn" :disabled="searching || !searchQuery.trim()">
              {{ searching ? '检索中…' : '检索' }}
            </button>
          </form>
          <div v-if="searchPerformed && !references.length" class="empty-small">没有达到相关度要求的切块</div>
          <article v-for="(reference, index) in references" :key="reference.chunk_id" class="reference-card">
            <div class="reference-head">
              <strong>[{{ index + 1 }}] {{ reference.document_name }}</strong>
              <span>{{ reference.heading || `切块 ${reference.chunk_index + 1}` }}</span>
            </div>
            <p>{{ reference.content }}</p>
            <small>字符 {{ reference.start_rune }}–{{ reference.end_rune }}<template v-if="reference.score"> · 相似度 {{ Number(reference.score).toFixed(4) }}</template></small>
          </article>
        </section>
      </section>

      <section v-else class="welcome-panel">
        <div aria-hidden="true">▣</div>
        <h2>选择或新建一个知识库</h2>
        <p>每个知识库可以保存多份文档，并独立管理索引和引用。</p>
      </section>
    </section>
  </main>
</template>

<script>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from '../utils/elementFeedback'
import api from '../utils/api'

export default {
  name: 'KnowledgeBaseView',
  setup() {
    const knowledgeBases = ref([])
    const selectedBase = ref(null)
    const documents = ref([])
    const references = ref([])
    const searchQuery = ref('')
    const loading = ref(false)
    const listError = ref('')
    const detailLoading = ref(false)
    const detailError = ref('')
    const uploading = ref(false)
    const searching = ref(false)
    const searchPerformed = ref(false)
    const fileInput = ref(null)
    let disposed = false
    let selectionVersion = 0
    let listRequestPromise = null
    let listAbortController = null
    let listRequestSerial = 0
    let detailAbortController = null
    let detailRequestSerial = 0
    let uploadAbortController = null
    let searchAbortController = null
    const documentPolls = new Map()

    const DOCUMENT_POLL_INTERVAL = 1500
    const MAX_DOCUMENT_POLL_INTERVAL = 15000
    const MAX_DOCUMENT_POLL_ATTEMPTS = 60

    const readyDocuments = computed(() => documents.value.filter(item => item.status === 'ready').length)
    const totalChunks = computed(() => documents.value.reduce((sum, item) => sum + Number(item.chunk_count || 0), 0))

    const isAbortError = error => error?.code === 'ERR_CANCELED' || error?.name === 'CanceledError' || error?.name === 'AbortError'
    const isPageVisible = () => typeof document === 'undefined' || document.visibilityState !== 'hidden'
    const baseIdOf = base => base?.id === undefined || base?.id === null ? '' : String(base.id)
    const currentBaseId = () => baseIdOf(selectedBase.value)
    const normalizeBase = base => ({ ...base, id: String(base.id) })
    const normalizeDocument = document => ({ ...document, id: String(document.id) })
    const isCurrentSelection = (baseId, version) => !disposed && selectionVersion === version && currentBaseId() === baseId
    const pollDelay = failures => {
      const base = Math.min(MAX_DOCUMENT_POLL_INTERVAL, DOCUMENT_POLL_INTERVAL * (2 ** Math.min(failures, 4)))
      return base + Math.round(Math.random() * Math.min(750, base * 0.15))
    }

    const assertSuccess = (response, fallback) => {
      if (!response?.data || response.data.status_code !== 1000) {
        throw new Error(response?.data?.status_msg || fallback)
      }
      return response.data
    }

    const stopDocumentPoll = state => {
      state.stopped = true
      if (state.timer !== null) window.clearTimeout(state.timer)
      state.timer = null
      state.controller?.abort()
      if (documentPolls.get(state.key) === state) documentPolls.delete(state.key)
    }

    const stopDocumentPolls = predicate => {
      documentPolls.forEach(state => {
        if (!predicate || predicate(state)) stopDocumentPoll(state)
      })
    }

    const scheduleDocumentPoll = (state, delay = DOCUMENT_POLL_INTERVAL) => {
      if (state.timer !== null) window.clearTimeout(state.timer)
      state.timer = null
      if (state.stopped || state.paused || !isPageVisible()) return
      if (!isCurrentSelection(state.baseId, state.selectionVersion)) {
        stopDocumentPoll(state)
        return
      }
      state.timer = window.setTimeout(() => {
        state.timer = null
        if (state.inFlight) {
          scheduleDocumentPoll(state, 100)
          return
        }
        void runDocumentPoll(state)
      }, delay)
    }

    const runDocumentPoll = async state => {
      if (state.stopped || state.paused || !isPageVisible()) return
      if (!isCurrentSelection(state.baseId, state.selectionVersion)) {
        stopDocumentPoll(state)
        return
      }

      state.inFlight = true
      const controller = new AbortController()
      state.controller = controller
      try {
        if (state.deleteTaskId) {
          const payload = assertSuccess(
            await api.get(`/file/index-tasks/${encodeURIComponent(state.deleteTaskId)}`, { signal: controller.signal }),
            '获取删除状态失败'
          )
          if (!isCurrentSelection(state.baseId, state.selectionVersion)) {
            stopDocumentPoll(state)
            return
          }
          const task = payload.index_task
          state.attempts += 1
          state.failures = 0
          if (task && task.status === 'succeeded') {
            stopDocumentPoll(state)
            documents.value = documents.value.filter(item => String(item.id) !== state.documentId)
            references.value = references.value.filter(item => String(item.document_id) !== state.documentId)
            void loadKnowledgeBases({ silent: true })
            ElMessage.success('文档已删除')
            return
          }
          if (task && ['failed', 'cancelled'].includes(task.status)) {
            stopDocumentPoll(state)
            void loadKnowledgeBases({ silent: true })
            ElMessage.error(task.error || '文档删除失败，请稍后重试')
            return
          }
          if (state.attempts >= MAX_DOCUMENT_POLL_ATTEMPTS) {
            stopDocumentPoll(state)
            ElMessage.error('文档删除状态查询超时，请稍后刷新')
            return
          }
          scheduleDocumentPoll(state, DOCUMENT_POLL_INTERVAL)
          return
        }
        const payload = assertSuccess(
          await api.get(
            `/file/knowledge-bases/${encodeURIComponent(state.baseId)}/documents/${encodeURIComponent(state.documentId)}/status`,
            { signal: controller.signal }
          ),
          '获取索引状态失败'
        )
        if (!isCurrentSelection(state.baseId, state.selectionVersion)) {
          stopDocumentPoll(state)
          return
        }

        const document = payload.document ? normalizeDocument(payload.document) : null
        const task = payload.index_task
        const index = documents.value.findIndex(item => String(item.id) === state.documentId)
        if (index >= 0 && document) documents.value[index] = document
        state.attempts += 1
        state.failures = 0

        // A page refresh loses the in-memory delete task ID. The document
        // status endpoint includes its latest task, so recover it and switch
        // to the delete-specific poll before treating the tombstone as a
        // normal indexing state.
        if (document?.status === 'deleting') {
          const deleteTaskId = task?.type === 'delete' ? (task.id || task.ID) : ''
          if (deleteTaskId) {
            state.deleteTaskId = String(deleteTaskId)
            scheduleDocumentPoll(state, 0)
            return
          }
        }

        if (document && ['ready', 'failed'].includes(document.status)) {
          stopDocumentPoll(state)
          void loadKnowledgeBases({ silent: true })
          return
        }
        if (state.attempts >= MAX_DOCUMENT_POLL_ATTEMPTS) {
          stopDocumentPoll(state)
          ElMessage.error('文档索引状态查询超时，请刷新后重试')
          return
        }
        scheduleDocumentPoll(state, DOCUMENT_POLL_INTERVAL)
      } catch (error) {
        if (isAbortError(error)) return
        if (!isCurrentSelection(state.baseId, state.selectionVersion)) {
          stopDocumentPoll(state)
          return
        }
        state.attempts += 1
        state.failures += 1
        if (state.attempts >= MAX_DOCUMENT_POLL_ATTEMPTS) {
          stopDocumentPoll(state)
          ElMessage.error('文档索引状态查询多次失败，请刷新后重试')
          return
        }
        scheduleDocumentPoll(state, pollDelay(state.failures))
      } finally {
        if (state.controller === controller) state.controller = null
        state.inFlight = false
        // A hidden-tab abort can finish after the tab becomes visible again.
        // Resume it here instead of silently dropping this document's poll.
        if (!state.stopped && !state.paused && isPageVisible() && isCurrentSelection(state.baseId, state.selectionVersion) && state.timer === null) {
          scheduleDocumentPoll(state, 0)
        }
      }
    }

    const pollDocument = (documentId, baseId = currentBaseId(), version = selectionVersion, deleteTaskId = '') => {
      const normalizedBaseId = String(baseId || '')
      const normalizedDocumentId = String(documentId || '')
      if (!normalizedBaseId || !normalizedDocumentId || !isCurrentSelection(normalizedBaseId, version)) return
      const key = `${normalizedBaseId}:${normalizedDocumentId}`
      if (documentPolls.has(key)) return
      const state = {
        key,
        baseId: normalizedBaseId,
        documentId: normalizedDocumentId,
        selectionVersion: version,
        attempts: 0,
        failures: 0,
        inFlight: false,
        paused: !isPageVisible(),
        stopped: false,
        timer: null,
        controller: null,
        deleteTaskId: String(deleteTaskId || '')
      }
      documentPolls.set(key, state)
      if (!state.paused) scheduleDocumentPoll(state, 0)
    }

    const pauseDocumentPolls = () => {
      documentPolls.forEach(state => {
        state.paused = true
        if (state.timer !== null) window.clearTimeout(state.timer)
        state.timer = null
        state.controller?.abort()
      })
    }

    const resumeDocumentPolls = () => {
      documentPolls.forEach(state => {
        if (state.stopped || !isCurrentSelection(state.baseId, state.selectionVersion)) {
          stopDocumentPoll(state)
          return
        }
        state.paused = false
        if (!state.inFlight) scheduleDocumentPoll(state, 0)
      })
    }

    const handleVisibilityChange = () => {
      if (isPageVisible()) resumeDocumentPolls()
      else pauseDocumentPolls()
    }

    const loadKnowledgeBases = async ({ silent = false } = {}) => {
      if (listRequestPromise) return listRequestPromise
      const requestSerial = ++listRequestSerial
      const controller = new AbortController()
      listAbortController = controller
      loading.value = true
      if (!silent) listError.value = ''
      let request
      request = (async () => {
        try {
          const payload = assertSuccess(
            await api.get('/file/knowledge-bases', {
              params: { page: 1, page_size: 100 },
              signal: controller.signal
            }),
            '获取知识库失败'
          )
          if (disposed || requestSerial !== listRequestSerial) return null
          knowledgeBases.value = Array.isArray(payload.knowledge_bases)
            ? payload.knowledge_bases.map(normalizeBase)
            : []

          const selectedId = currentBaseId()
          if (selectedId) {
            const updated = knowledgeBases.value.find(item => String(item.id) === selectedId)
            if (updated) selectedBase.value = { ...selectedBase.value, ...updated }
            else {
              selectionVersion += 1
              stopDocumentPolls()
              detailAbortController?.abort()
              selectedBase.value = null
              documents.value = []
              references.value = []
              detailError.value = ''
            }
          }
          if (!selectedBase.value && knowledgeBases.value.length) await selectKnowledgeBase(knowledgeBases.value[0])
          return true
        } catch (error) {
          if (isAbortError(error) || disposed || requestSerial !== listRequestSerial) return null
          if (!silent) listError.value = error.message || '获取知识库失败'
          return false
        } finally {
          if (requestSerial === listRequestSerial) {
            loading.value = false
            listAbortController = null
          }
          if (listRequestPromise === request) listRequestPromise = null
        }
      })()
      listRequestPromise = request
      return request
    }

    const selectKnowledgeBase = async (base, { force = false } = {}) => {
      const nextBase = normalizeBase(base)
      const baseId = baseIdOf(nextBase)
      if (!baseId) return null
      if (!force && currentBaseId() === baseId && !detailError.value) return true

      selectionVersion += 1
      const version = selectionVersion
      stopDocumentPolls()
      detailAbortController?.abort()
      searchAbortController?.abort()
      selectedBase.value = nextBase
      documents.value = []
      references.value = []
      searchPerformed.value = false
      searching.value = false
      detailError.value = ''
      detailLoading.value = true
      const requestSerial = ++detailRequestSerial
      const controller = new AbortController()
      detailAbortController = controller
      try {
        const payload = assertSuccess(
          await api.get(`/file/knowledge-bases/${encodeURIComponent(baseId)}`, { signal: controller.signal }),
          '获取知识库详情失败'
        )
        if (!isCurrentSelection(baseId, version) || requestSerial !== detailRequestSerial) return null
        selectedBase.value = normalizeBase(payload.knowledge_base || nextBase)
        const loadedDocuments = Array.isArray(payload.documents)
          ? payload.documents
          : (Array.isArray(payload.knowledge_base?.documents) ? payload.knowledge_base.documents : [])
        documents.value = loadedDocuments.map(normalizeDocument)
        documents.value
          .filter(item => ['pending', 'indexing', 'deleting'].includes(item.status))
          .forEach(item => pollDocument(item.id, baseId, version))
        return true
      } catch (error) {
        if (isAbortError(error) || !isCurrentSelection(baseId, version) || requestSerial !== detailRequestSerial) return null
        detailError.value = error.message || '获取知识库详情失败'
        return false
      } finally {
        if (requestSerial === detailRequestSerial) {
          detailLoading.value = false
          detailAbortController = null
        }
      }
    }

    const retrySelectedKnowledgeBase = () => selectedBase.value && selectKnowledgeBase(selectedBase.value, { force: true })

    const createKnowledgeBase = async () => {
      try {
        const { value } = await ElMessageBox.prompt('请输入知识库名称', '新建知识库', {
          confirmButtonText: '创建', cancelButtonText: '取消', inputPlaceholder: '例如：项目技术文档',
          inputValidator: input => input.trim().length > 0 && input.trim().length <= 100 || '名称长度应为 1–100 个字符'
        })
        const payload = assertSuccess(await api.post('/file/knowledge-bases', { name: value.trim(), description: '' }), '创建知识库失败')
        const base = normalizeBase(payload.knowledge_base)
        knowledgeBases.value.unshift(base)
        await selectKnowledgeBase(base)
        ElMessage.success('知识库已创建')
      } catch (error) {
        if (error !== 'cancel' && error !== 'close') ElMessage.error(error.message || '创建知识库失败')
      }
    }

    const deleteKnowledgeBase = async () => {
      if (!selectedBase.value) return
      const baseId = currentBaseId()
      const baseName = selectedBase.value.name
      try {
        await ElMessageBox.confirm(`确定删除“${baseName}”及其全部文档吗？`, '删除知识库', {
          type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消'
        })
        assertSuccess(await api.delete(`/file/knowledge-bases/${encodeURIComponent(baseId)}`), '删除知识库失败')
        knowledgeBases.value = knowledgeBases.value.filter(item => String(item.id) !== baseId)
        if (currentBaseId() === baseId) {
          selectionVersion += 1
          stopDocumentPolls()
          detailAbortController?.abort()
          searchAbortController?.abort()
          selectedBase.value = null
          documents.value = []
          references.value = []
          detailError.value = ''
        }
        if (!selectedBase.value && knowledgeBases.value.length) await selectKnowledgeBase(knowledgeBases.value[0])
        ElMessage.success('知识库已删除')
      } catch (error) {
        if (error !== 'cancel' && error !== 'close') ElMessage.error(error.message || '删除知识库失败')
      }
    }

    const uploadDocument = async (event) => {
      const file = event.target.files?.[0]
      if (!file || !selectedBase.value) return
      const baseId = currentBaseId()
      const version = selectionVersion
      const lowerName = file.name.toLowerCase()
      if (!lowerName.endsWith('.md') && !lowerName.endsWith('.txt')) {
        ElMessage.error('当前 P0 支持 Markdown 和文本文件')
        event.target.value = ''
        return
      }
      uploading.value = true
      const controller = new AbortController()
      uploadAbortController = controller
      try {
        const form = new FormData()
        form.append('file', file)
        const payload = assertSuccess(
          await api.post(`/file/knowledge-bases/${encodeURIComponent(baseId)}/documents`, form, { signal: controller.signal }),
          '上传文档失败'
        )
        if (!isCurrentSelection(baseId, version)) return
        const document = normalizeDocument(payload.document)
        documents.value.unshift(document)
        ElMessage.success('文档已上传，正在后台建立索引')
        pollDocument(document.id, baseId, version)
      } catch (error) {
        if (!isAbortError(error)) ElMessage.error(error.message || '上传文档失败')
      } finally {
        if (uploadAbortController === controller) {
          uploading.value = false
          uploadAbortController = null
        }
        event.target.value = ''
      }
    }

    const deleteDocument = async (document) => {
      if (!selectedBase.value) return
      const baseId = currentBaseId()
      const version = selectionVersion
      const documentId = String(document.id)
      try {
        await ElMessageBox.confirm(`确定删除文档“${document.name}”吗？`, '删除文档', {
          type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消'
        })
        const payload = assertSuccess(await api.delete(`/file/knowledge-bases/${encodeURIComponent(baseId)}/documents/${encodeURIComponent(documentId)}`), '删除文档失败')
        if (!isCurrentSelection(baseId, version)) return
        stopDocumentPolls(state => state.baseId === baseId && state.documentId === documentId)
        const index = documents.value.findIndex(item => String(item.id) === documentId)
        if (index >= 0) documents.value[index] = { ...documents.value[index], status: 'deleting', error: '' }
        references.value = references.value.filter(item => String(item.document_id) !== documentId)
        const taskId = payload.index_task?.id || payload.index_task?.ID
        if (taskId) pollDocument(documentId, baseId, version, taskId)
        ElMessage.success('文档删除已排队')
      } catch (error) {
        if (error !== 'cancel' && error !== 'close') ElMessage.error(error.message || '删除文档失败')
      }
    }

    const searchKnowledgeBase = async () => {
      if (!selectedBase.value || !searchQuery.value.trim()) return
      const baseId = currentBaseId()
      const version = selectionVersion
      searching.value = true
      searchPerformed.value = false
      searchAbortController?.abort()
      const controller = new AbortController()
      searchAbortController = controller
      try {
        const payload = assertSuccess(await api.post(`/file/knowledge-bases/${encodeURIComponent(baseId)}/search`, {
          query: searchQuery.value.trim(), top_k: 5
        }, { signal: controller.signal }), '知识库检索失败')
        if (!isCurrentSelection(baseId, version)) return
        references.value = Array.isArray(payload.references) ? payload.references : []
        searchPerformed.value = true
      } catch (error) {
        if (!isAbortError(error) && isCurrentSelection(baseId, version)) ElMessage.error(error.message || '知识库检索失败')
      } finally {
        if (searchAbortController === controller) {
          searching.value = false
          searchAbortController = null
        }
      }
    }

    const formatBytes = bytes => {
      const value = Number(bytes || 0)
      if (value < 1024) return `${value} B`
      if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`
      return `${(value / 1024 / 1024).toFixed(1)} MB`
    }
    const formatDate = value => value ? new Date(value).toLocaleString() : '—'
    const statusLabel = status => ({ pending: '等待索引', indexing: '索引中', ready: '已就绪', deleting: '删除中', failed: '失败' }[status] || status)

    onMounted(() => {
      document.addEventListener('visibilitychange', handleVisibilityChange)
      void loadKnowledgeBases()
    })
    onBeforeUnmount(() => {
      disposed = true
      document.removeEventListener('visibilitychange', handleVisibilityChange)
      listAbortController?.abort()
      detailAbortController?.abort()
      uploadAbortController?.abort()
      searchAbortController?.abort()
      stopDocumentPolls()
    })

    return {
      knowledgeBases, selectedBase, documents, references, searchQuery, loading, listError, detailLoading, detailError, uploading, searching,
      searchPerformed, fileInput, readyDocuments, totalChunks, loadKnowledgeBases, selectKnowledgeBase, retrySelectedKnowledgeBase,
      createKnowledgeBase, deleteKnowledgeBase, uploadDocument, deleteDocument, searchKnowledgeBase,
      formatBytes, formatDate, statusLabel
    }
  }
}
</script>

<style scoped>
/*
 * Knowledge Base — frosted window header, a sunken library sidebar, and a
 * detail column built from resting cards. Index state (pending / indexing /
 * ready / failed) is carried by fill weight rather than hue: solid black reads
 * as done, a muted grey fill as in progress, a dashed outline as failed.
 */

.knowledge-page {
  min-height: 100vh;
  color: var(--mac-text);
  background: var(--mac-bg);
}

button {
  font: inherit;
}

button:disabled {
  opacity: 0.45;
}

/* ---- Window header ------------------------------------------------------ */

.page-header {
  display: grid;
  grid-template-columns: auto 1fr auto;
  align-items: center;
  gap: var(--mac-space-5);
  min-height: 82px;
  padding: 16px 30px;
  border-bottom: 1px solid var(--mac-border-soft);
  background: var(--mac-material-strong);
  -webkit-backdrop-filter: var(--mac-material-blur);
  backdrop-filter: var(--mac-material-blur);
}

.page-header p,
.detail-header p,
.search-title p {
  margin-top: 4px;
  color: var(--mac-text-secondary);
  font-size: var(--mac-text-base);
}

/* ---- Controls ----------------------------------------------------------- */

.back-btn,
.icon-btn,
.text-btn {
  border: 0;
  color: var(--mac-text-secondary);
  background: transparent;
  transition: color var(--mac-dur-fast) var(--mac-ease),
    background-color var(--mac-dur-fast) var(--mac-ease);
}

.back-btn {
  padding: 8px 12px;
  border-radius: var(--mac-radius-control);
}

.icon-btn {
  padding: 5px 9px;
  border-radius: var(--mac-radius-control);
  font-size: var(--mac-text-base);
}

.text-btn {
  padding: 4px 8px;
  border-radius: var(--mac-radius-xs);
  font-size: var(--mac-text-base);
  font-weight: 590;
}

.back-btn:hover,
.icon-btn:hover,
.text-btn:hover {
  color: var(--mac-text);
  background: var(--mac-surface-strong);
}

.primary-btn,
.upload-btn,
.danger-btn {
  padding: 9px 16px;
  border: 1px solid transparent;
  border-radius: var(--mac-radius-control);
  font-size: var(--mac-text-md);
  font-weight: 590;
  cursor: pointer;
  transition: background-color var(--mac-dur-fast) var(--mac-ease),
    border-color var(--mac-dur-fast) var(--mac-ease);
}

.primary-btn,
.upload-btn {
  color: var(--mac-text-inverse);
  background: var(--mac-black);
}

.primary-btn:hover:not(:disabled),
.upload-btn:hover:not(:disabled) {
  background: var(--mac-black-hover);
}

.primary-btn:active:not(:disabled),
.upload-btn:active:not(:disabled) {
  background: var(--mac-black-active);
}

/*
 * The destructive action stays monochrome. What separates it from the primary
 * button is weight, not hue: it is an outlined control next to a filled one,
 * so the filled button remains the one the eye lands on first.
 */
.danger-btn {
  border-color: var(--mac-border);
  color: var(--mac-text);
  background: var(--mac-surface);
  box-shadow: var(--mac-elev-1);
}

.danger-btn:hover:not(:disabled) {
  border-color: var(--mac-border-strong);
  background: var(--mac-surface-muted);
}

/* ---- Workspace shell ---------------------------------------------------- */

.workspace {
  display: grid;
  grid-template-columns: 270px minmax(0, 1fr);
  min-height: calc(100vh - 82px);
}

.library-panel {
  padding: 12px 10px;
  border-right: 1px solid var(--mac-border-soft);
  background: var(--mac-bg-sunken);
}

.panel-title {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 6px 6px 10px;
  color: var(--mac-text-secondary);
  font-size: var(--mac-text-base);
  font-weight: 650;
  letter-spacing: var(--mac-tracking-tight);
}

.library-item {
  display: flex;
  flex-direction: column;
  gap: 3px;
  width: 100%;
  margin-bottom: var(--mac-space-1);
  padding: 10px 12px;
  border: 1px solid transparent;
  border-radius: var(--mac-radius-md);
  color: var(--mac-text-secondary);
  text-align: left;
  background: transparent;
  cursor: pointer;
  transition: color var(--mac-dur-fast) var(--mac-ease),
    background-color var(--mac-dur-fast) var(--mac-ease);
}

.library-item:hover {
  color: var(--mac-text);
  background: var(--mac-surface-strong);
}

.library-item.active {
  border-color: var(--mac-border-soft);
  color: var(--mac-text);
  background: var(--mac-surface);
  box-shadow: var(--mac-elev-1);
}

.library-name {
  font-size: var(--mac-text-md);
  font-weight: 590;
}

.library-meta {
  color: var(--mac-text-tertiary);
  font-size: var(--mac-text-sm);
  font-variant-numeric: tabular-nums;
}

/* ---- Detail column ------------------------------------------------------ */

.detail-panel {
  padding: 26px clamp(16px, 3vw, 28px);
  overflow: hidden;
}

.detail-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--mac-space-5);
}

.header-actions {
  display: flex;
  gap: var(--mac-space-2);
}

.stats-row {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: var(--mac-space-3);
  margin: var(--mac-space-6) 0;
}

.stat-card {
  display: flex;
  align-items: baseline;
  gap: var(--mac-space-2);
  padding: 16px 18px;
  border: 1px solid var(--mac-border-soft);
  border-radius: var(--mac-radius-lg);
  background: var(--mac-surface);
  box-shadow: var(--mac-elev-1);
}

.stat-card strong {
  color: var(--mac-text);
  font-size: var(--mac-text-2xl);
  font-weight: 650;
  letter-spacing: var(--mac-tracking-tight);
  font-variant-numeric: tabular-nums;
}

.stat-card span {
  color: var(--mac-text-secondary);
  font-size: var(--mac-text-base);
}

.documents-section,
.search-section {
  margin-top: var(--mac-space-5);
  padding: 20px 22px;
  border: 1px solid var(--mac-border-soft);
  border-radius: var(--mac-radius-xl);
  background: var(--mac-surface);
  box-shadow: var(--mac-elev-1);
}

.documents-section h3,
.search-section h3 {
  font-size: var(--mac-text-lg);
}

.documents-section h3 {
  margin-bottom: var(--mac-space-2);
}

/* ---- Documents ---------------------------------------------------------- */

.document-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto auto auto;
  align-items: center;
  gap: var(--mac-space-3);
  padding: 12px 2px;
  border-top: 1px solid var(--mac-border-soft);
}

.document-main {
  display: flex;
  flex-direction: column;
  gap: 3px;
  min-width: 0;
}

.document-main strong {
  overflow: hidden;
  font-weight: 590;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.document-main span,
.chunk-count {
  color: var(--mac-text-tertiary);
  font-size: var(--mac-text-sm);
}

.chunk-count {
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}

.status-pill {
  padding: 4px 9px;
  border: 1px solid transparent;
  border-radius: var(--mac-radius-full);
  color: var(--mac-text-secondary);
  background: var(--mac-surface-strong);
  font-size: var(--mac-text-sm);
  white-space: nowrap;
}

/* Work still in flight: the quietest weight, a muted fill with no edge. */
.status-pending,
.status-indexing,
.status-deleting {
  color: var(--mac-text-secondary);
  background: var(--mac-surface-strong);
}

/* Indexed and usable: full solid fill, the heaviest state on the page. */
.status-ready {
  color: var(--mac-text-inverse);
  background: var(--mac-black);
}

/* Unavailable: dashed outline, matching how disabled capabilities read
 * elsewhere in the system. */
.status-failed {
  border-style: dashed;
  border-color: var(--mac-border-strong);
  color: var(--mac-text);
  background: transparent;
}

.danger-text {
  color: var(--mac-text-tertiary);
}

.danger-text:hover {
  color: var(--mac-text);
  background: var(--mac-surface-strong);
}

.document-error {
  grid-column: 1 / -1;
  color: var(--mac-text-secondary);
  font-size: var(--mac-text-sm);
}

/* ---- Retrieval debugger ------------------------------------------------- */

.search-title {
  display: flex;
  justify-content: space-between;
  gap: var(--mac-space-3);
}

.search-form {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: var(--mac-space-2);
  margin: var(--mac-space-4) 0;
}

.search-form input {
  min-width: 0;
  padding: 9px 12px;
  border: 1px solid var(--mac-border);
  border-radius: var(--mac-radius-control);
  color: var(--mac-text);
  background: var(--mac-surface);
  outline: none;
  transition: border-color var(--mac-dur-fast) var(--mac-ease),
    box-shadow var(--mac-dur-fast) var(--mac-ease);
}

.search-form input::placeholder {
  color: var(--mac-text-quaternary);
}

.search-form input:hover {
  border-color: var(--mac-border-strong);
}

.search-form input:focus {
  border-color: var(--mac-black);
  box-shadow: var(--mac-focus-ring);
}

.reference-card {
  margin-top: var(--mac-space-2);
  padding: 14px;
  border: 1px solid var(--mac-border-soft);
  border-radius: var(--mac-radius-lg);
  background: var(--mac-surface-muted);
}

.reference-head {
  display: flex;
  justify-content: space-between;
  gap: var(--mac-space-3);
  margin-bottom: 6px;
}

.reference-head strong {
  font-weight: 590;
}

.reference-head span {
  color: var(--mac-text-tertiary);
  font-size: var(--mac-text-base);
  white-space: nowrap;
}

.reference-card p {
  color: var(--mac-text);
  line-height: 1.6;
  white-space: pre-wrap;
}

.reference-card small {
  display: block;
  margin-top: var(--mac-space-2);
  color: var(--mac-text-tertiary);
  font-size: var(--mac-text-sm);
  font-variant-numeric: tabular-nums;
}

/* ---- Status strips & empty states --------------------------------------- */

.list-error,
.detail-error,
.detail-status {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--mac-space-3);
  padding: 9px 12px;
  border: 1px solid var(--mac-border-soft);
  border-radius: var(--mac-radius-md);
  font-size: var(--mac-text-base);
}

/*
 * A solid leading edge is what marks the failure strips, mirroring the error
 * toast in the Element Plus theme — no second hue needed to make them read as
 * more urgent than the neutral loading strip.
 */
.list-error,
.detail-error {
  border-left: 3px solid var(--mac-black);
  color: var(--mac-text);
  background: var(--mac-surface);
}

.detail-status {
  margin-top: var(--mac-space-3);
  color: var(--mac-text-secondary);
  background: var(--mac-surface-inset);
}

.list-error {
  margin: 0 6px 10px;
}

.empty-small {
  padding: 20px 8px;
  color: var(--mac-text-tertiary);
  font-size: var(--mac-text-base);
  text-align: center;
}

.empty-box,
.welcome-panel {
  display: grid;
  place-items: center;
  gap: var(--mac-space-2);
  color: var(--mac-text-tertiary);
  text-align: center;
}

.empty-box {
  min-height: 130px;
}

.empty-box span,
.welcome-panel > div {
  color: var(--mac-text-quaternary);
  font-family: var(--mac-font-mono);
  font-size: 34px;
  line-height: 1;
}

.welcome-panel {
  align-self: center;
  padding: 40px;
}

.welcome-panel h2 {
  color: var(--mac-text);
}

.welcome-panel p {
  max-width: 46ch;
  color: var(--mac-text-secondary);
}

/* ---- Responsive --------------------------------------------------------- */

@media (max-width: 850px) {
  .page-header {
    grid-template-columns: auto 1fr;
    gap: var(--mac-space-3);
    padding: 14px;
  }

  .page-header > .primary-btn {
    grid-column: 1 / -1;
  }

  .workspace {
    grid-template-columns: 1fr;
    min-height: 0;
  }

  .library-panel {
    display: flex;
    gap: var(--mac-space-2);
    padding: 10px;
    border-right: 0;
    border-bottom: 1px solid var(--mac-border-soft);
    overflow-x: auto;
  }

  .panel-title {
    min-width: 120px;
    padding: 0;
  }

  .library-item {
    min-width: 190px;
    margin-bottom: 0;
  }

  .list-error {
    margin: 0;
    min-width: 220px;
  }

  .detail-panel {
    padding: 16px;
  }

  .detail-header {
    flex-direction: column;
  }

  .stats-row {
    grid-template-columns: 1fr;
    margin: var(--mac-space-4) 0;
  }

  .documents-section,
  .search-section {
    padding: 16px;
  }

  .document-row {
    grid-template-columns: minmax(0, 1fr) auto;
  }

  .chunk-count {
    display: none;
  }
}
</style>
