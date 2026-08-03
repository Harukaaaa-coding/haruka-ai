<template>
  <main class="ai-chat-container" :aria-busy="loading">
    <!-- 左侧会话列表 -->
    <aside class="session-list" aria-label="聊天会话">
      <div class="session-list-header">
        <span>会话列表</span>
        <button type="button" class="new-chat-btn" @click="createNewSession">＋ 新聊天</button>
      </div>
      <div v-if="sessionsLoading && !sessions.length" class="session-list-state" role="status">正在加载会话…</div>
      <div v-else-if="sessionsError" class="session-list-error" role="alert">
        <span>{{ sessionsError }}</span>
        <button type="button" class="session-retry-btn" :disabled="sessionsLoading" @click="retryLoadSessions">重试</button>
      </div>
      <div v-else-if="!sessions.length" class="session-list-state">还没有历史会话</div>
      <ul v-else class="session-list-ul">
        <li
          v-for="session in sessions"
          :key="session.id"
          :class="['session-item', { active: currentSessionId === session.id }]"
          role="button"
          tabindex="0"
          :aria-current="currentSessionId === session.id ? 'true' : undefined"
          @click="switchSession(session.id)"
          @keydown.enter.prevent="switchSession(session.id)"
          @keydown.space.prevent="switchSession(session.id)"
        >
          {{ session.name || `会话 ${session.id}` }}
        </li>
      </ul>
      <div v-if="sessionsMoreError" class="session-list-error session-list-more-error" role="alert">
        <span>{{ sessionsMoreError }}</span>
        <button type="button" class="session-retry-btn" :disabled="sessionsLoadingMore" @click="loadMoreSessions">重试</button>
      </div>
      <button
        v-if="sessionsHasMore"
        type="button"
        class="load-more-sessions-btn"
        :disabled="sessionsLoadingMore"
        @click="loadMoreSessions"
      >{{ sessionsLoadingMore ? '正在加载更多…' : '加载更多会话' }}</button>
    </aside>

    <!-- 右侧聊天区域 -->
    <div class="chat-section">
      <header class="top-bar">
        <button type="button" class="back-btn" @click="$router.push('/menu')">← 返回</button>
        <button type="button" class="sync-btn" @click="syncHistory" :disabled="!currentSessionId || tempSession || loading || realtimeVoiceTurnActive">同步历史数据</button>
        <label for="modelType" class="model-label">选择模型：</label>
        <select id="modelType" v-model="selectedModel" class="model-select" :disabled="loading || modelsLoading">
          <option v-for="model in modelOptions" :key="model.id" :value="model.id">{{ model.displayName }}</option>
        </select>
        <div v-if="isRagPipeline" class="knowledge-base-control">
          <label for="knowledgeBase">知识库：</label>
          <select
            id="knowledgeBase"
            v-model="selectedKnowledgeBaseId"
            class="knowledge-base-select"
            :disabled="loading || knowledgeBasesLoading"
          >
            <option value="">全部知识库</option>
            <option v-for="base in knowledgeBases" :key="base.id" :value="String(base.id)">
              {{ base.optionLabel }}
            </option>
          </select>
          <span v-if="knowledgeBasesLoading" class="knowledge-loading">加载中…</span>
        </div>
        <label for="streamingMode" class="streaming-label">
          <input type="checkbox" id="streamingMode" v-model="isStreaming" :disabled="loading" />
          流式响应
        </label>
        <button type="button" class="upload-btn" @click="triggerFileUpload" :disabled="uploading">{{ uploading ? '上传中...' : '上传文档' }}</button>
        <input
          ref="fileInput"
          type="file"
          accept=".md,.txt,text/markdown,text/plain"
          style="display: none"
          aria-label="选择 Markdown 或文本文件"
          @change="handleFileUpload"
        />
      </header>

      <p class="visually-hidden" role="status" aria-live="polite" aria-atomic="true">{{ chatAnnouncement }}</p>
      <div class="chat-messages" ref="messagesRef" role="region" aria-label="聊天记录" aria-live="off">
        <div v-if="sessionHistoryLoading" class="chat-history-state" role="status">正在加载会话历史…</div>
        <div v-else-if="sessionHistoryError" class="chat-history-error" role="alert">
          <span>{{ sessionHistoryError }}</span>
          <button type="button" class="history-retry-btn" @click="retryCurrentSessionHistory">重试</button>
        </div>
        <template v-else>
          <div v-if="currentSessionHasMore || historyLoadingMore || historyMoreError" class="history-load-more">
            <span v-if="historyMoreError" class="history-more-error" role="alert">{{ historyMoreError }}</span>
            <button
              v-if="currentSessionHasMore"
              type="button"
              class="history-retry-btn"
              :disabled="historyLoadingMore || loading"
              @click="loadMoreSessionHistory"
            >{{ historyLoadingMore ? '正在加载更早消息…' : '加载更早消息' }}</button>
          </div>
          <div
          v-for="(message, index) in currentMessages"
          :key="message.key || index"
          :class="['message', message.role === 'user' ? 'user-message' : 'ai-message']"
        >
          <div class="message-header">
            <b>{{ message.role === 'user' ? '你' : 'AI' }}:</b>
            <button
              v-if="message.role === 'assistant'"
              type="button"
              class="tts-btn"
              aria-label="朗读这条 AI 回复"
              title="朗读这条 AI 回复"
              @click="playTTS(message.content)"
            >朗读</button>
            <span v-if="message.meta && message.meta.status === 'streaming'" class="streaming-indicator" aria-hidden="true"> ··</span>
          </div>
          <div class="message-content" v-html="renderMarkdown(message.content)"></div>
          <section
            v-if="message.role === 'assistant' && Array.isArray(message.citations) && message.citations.length"
            class="citation-section"
            aria-label="回答来源"
          >
            <div class="citation-title">参考来源</div>
            <article v-for="(citation, citationIndex) in message.citations" :key="citation.id || citationIndex" class="citation-card">
              <div class="citation-card-header">
                <strong><span class="citation-number">{{ citationIndex + 1 }}</span>{{ citation.documentName }}</strong>
                <span v-if="citation.scoreLabel" class="citation-score">相关度 {{ citation.scoreLabel }}</span>
              </div>
              <div v-if="citation.heading" class="citation-heading">{{ citation.heading }}</div>
              <p v-if="citation.content" class="citation-content">{{ citation.content }}</p>
              <div v-if="citation.location" class="citation-location">{{ citation.location }}</div>
            </article>
          </section>
          </div>
        </template>
      </div>

      <div class="chat-input">
        <textarea
          v-model="inputMessage"
          :placeholder="isRecording ? '正在录音，点击停止后自动识别…' : (transcribing ? '正在识别语音…' : '请输入你的问题...')"
          aria-label="聊天消息"
          @keydown.enter.exact.prevent="sendMessage"
          :disabled="loading || transcribing || realtimeVoiceTurnActive || sessionHistoryLoading || historyLoadingMore"
          ref="messageInput"
          rows="1"
        ></textarea>
        <button
          type="button"
          :class="['mic-btn', { recording: isRecording, processing: realtimeVoiceTurnActive && !isRecording }]"
          :disabled="(loading || transcribing || sessionHistoryLoading || historyLoadingMore) && !realtimeVoiceTurnActive"
          :aria-label="isRecording ? '停止录音并发送' : (realtimeVoiceTurnActive ? '中断语音对话' : '开始语音输入')"
          :title="isRecording ? '停止录音并发送' : (realtimeVoiceTurnActive ? '中断语音对话' : '开始语音输入')"
          @click="toggleRecording"
        >{{ isRecording ? '■' : (realtimeVoiceTurnActive ? '停止' : (transcribing ? '…' : '语音')) }}</button>
        <button
          v-if="loading && isStreaming"
          type="button"
          @click="stopStreaming"
          class="send-btn"
        >
          停止生成
        </button>
        <button
          v-else
          type="button"
          :disabled="!inputMessage.trim() || loading || realtimeVoiceTurnActive || sessionHistoryLoading || historyLoadingMore"
          @click="sendMessage"
          class="send-btn"
        >
          {{ loading ? '发送中...' : '发送' }}
        </button>
      </div>
    </div>
  </main>
</template>

<script>


import { ref, nextTick, computed, watch, onMounted, onBeforeUnmount } from 'vue'
import { ElMessage } from '../utils/elementFeedback'
import api, { buildApiUrl, csrfHeaders, handleUnauthorized, isAuthFailure } from '../utils/api'
import { VoiceTurnClient, VoiceTurnError, createTurnID, realtimeVoiceURL } from '../utils/voiceTurn'

// The backend's default model deadline is longer than Axios's generic API
// timeout. Non-streaming chat must not fail locally before the model gets its
// normal response window.
const normalChatTimeout = 120_000

export default {
  name: 'AIChat',
  setup() {

    const sessions = ref({})
    const sessionsLoading = ref(false)
    const sessionsError = ref('')
    const sessionsHasMore = ref(false)
    const sessionsNextCursor = ref('')
    const sessionsLoadingMore = ref(false)
    const sessionsMoreError = ref('')
    const currentSessionId = ref(null)
    const tempSession = ref(false)
    const currentMessages = ref([])
    const sessionHistoryLoading = ref(false)
    const sessionHistoryError = ref('')
    const historyLoadingMore = ref(false)
    const historyMoreError = ref('')
    const chatAnnouncement = ref('')
    const inputMessage = ref('')
    const loading = ref(false)
    const messagesRef = ref(null)
    const messageInput = ref(null)
    const selectedModel = ref('4')
    const modelsLoading = ref(false)
    const modelOptions = ref([
      { id: '1', displayName: '外部模型（OpenAI Compatible）', pipeline: 'chat', aliases: ['1'] },
      { id: '2', displayName: '本地 RAG（Ollama + Redis）', pipeline: 'rag', aliases: ['2'] },
      { id: '3', displayName: '本地 MCP（天气工具）', pipeline: 'mcp', aliases: ['3'] },
      { id: '4', displayName: '本地 Ollama', pipeline: 'chat', aliases: ['4'] }
    ])
    const knowledgeBases = ref([])
    const knowledgeBasesLoading = ref(false)
    const knowledgeBasesLoaded = ref(false)
    const selectedKnowledgeBaseId = ref('')
    const isStreaming = ref(false)
    const uploading = ref(false)
    const fileInput = ref(null)
    const isRecording = ref(false)
    const transcribing = ref(false)
    const realtimeVoiceTurnActive = ref(false)


    let activeStreamController = null
    let recordingContext = null
    let recordingStream = null
    let recordingSource = null
    let recordingProcessor = null
    let recordingSink = null
    let recordingTimer = null
    let recordingChunks = []
    let voiceTurnClient = null
    let voiceTurnState = null
    let disposed = false
    let sessionsAbortController = null
    let sessionsRequestPromise = null
    let sessionsRequestSerial = 0
    let historyAbortController = null
    let historyRequestPromise = null
    let historyRequestSerial = 0
    let historyRequestSessionId = ''
    let localMessageKeySequence = 0
    let streamRenderFrame = null
    let streamRefreshPromise = null
    let resolveStreamRefresh = null
    let announcementTimer = null

    const isAbortError = error => error?.code === 'ERR_CANCELED' || error?.name === 'CanceledError' || error?.name === 'AbortError'
    const sessionPageSize = 40
    const historyPageSize = 80
    const paginationFrom = payload => {
      const hasMoreValue = payload?.hasMore ?? payload?.has_more
      const cursorValue = payload?.nextCursor ?? payload?.next_cursor
      const hasMore = hasMoreValue === true || hasMoreValue === 1 || hasMoreValue === 'true'
      const nextCursor = cursorValue === null || cursorValue === undefined ? '' : String(cursorValue)
      return {
        hasMore: hasMore && Boolean(nextCursor),
        nextCursor
      }
    }
    const nextMessageKey = item => {
      const persistedID = item?.messageId ?? item?.message_id ?? item?.id
      if (persistedID !== null && persistedID !== undefined && persistedID !== '') return `message-${persistedID}`
      localMessageKeySequence += 1
      return `local-message-${localMessageKeySequence}`
    }
    const announceChat = message => {
      if (announcementTimer !== null) window.clearTimeout(announcementTimer)
      chatAnnouncement.value = ''
      announcementTimer = window.setTimeout(() => {
        if (!disposed) chatAnnouncement.value = message
        announcementTimer = null
      }, 40)
    }

    const selectedModelOption = computed(() => modelOptions.value.find(model => String(model.id) === String(selectedModel.value)))
    const currentSessionHasMore = computed(() => {
      if (tempSession.value || !currentSessionId.value) return false
      return Boolean(sessions.value[String(currentSessionId.value)]?.historyHasMore)
    })
    const isRagPipeline = computed(() => {
      const model = selectedModelOption.value
      if (!model) return false
      return model.pipeline === 'rag' || model.id === 'rag' || (model.aliases || []).map(String).includes('2')
    })

    const compatibleModelType = () => {
      const model = selectedModelOption.value
      if (!model) return String(selectedModel.value)
      const aliases = Array.isArray(model.aliases) ? model.aliases.map(String) : []
      return String(model.legacyModelType || model.modelType || aliases.find(alias => /^\d+$/.test(alias)) || model.id)
    }

    const buildChatSelection = () => ({
      modelId: String(selectedModel.value),
      modelType: compatibleModelType(),
      knowledgeBaseIds: isRagPipeline.value && selectedKnowledgeBaseId.value
        ? [String(selectedKnowledgeBaseId.value)]
        : []
    })

    const numberOrNull = (value) => {
      if (value === '' || value === null || value === undefined) return null
      const number = Number(value)
      return Number.isFinite(number) ? number : null
    }

    const normalizeCitations = (citations) => {
      if (!Array.isArray(citations)) return []
      return citations.filter(Boolean).map((citation, index) => {
        if (typeof citation === 'string') {
          return {
            id: `citation-${index}-${citation}`,
            documentName: citation,
            heading: '',
            content: '',
            location: '',
            scoreLabel: ''
          }
        }

        const chunkIndex = numberOrNull(citation.chunk_index ?? citation.chunkIndex)
        const startRune = numberOrNull(citation.start_rune ?? citation.startRune)
        const endRune = numberOrNull(citation.end_rune ?? citation.endRune)
        const score = numberOrNull(citation.score ?? citation.relevance_score ?? citation.relevanceScore)
        const locationParts = []
        if (chunkIndex !== null) locationParts.push(`分块 ${chunkIndex + 1}`)
        if (startRune !== null && endRune !== null) locationParts.push(`字符 ${startRune}–${endRune}`)

        return {
          id: String(citation.chunk_id ?? citation.chunkId ?? citation.id ?? `citation-${index}`),
          documentName: String(citation.document_name ?? citation.documentName ?? citation.source ?? citation.title ?? '未命名来源'),
          heading: String(citation.heading ?? citation.section ?? ''),
          content: String(citation.content ?? citation.snippet ?? citation.excerpt ?? citation.quote ?? ''),
          location: locationParts.join(' · '),
          scoreLabel: score === null ? '' : `${Math.round((score <= 1 ? score * 100 : score) * 10) / 10}%`
        }
      })
    }

    const responseContent = (payload) => String(payload?.Information ?? payload?.content ?? payload?.answer ?? '')

    const escapeHtml = (text) => String(text)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#039;')

    const renderMarkdown = (text) => {
      if (text === null || text === undefined) return ''

      const codeSegments = []
      const escaped = escapeHtml(text).replace(/`([^`\n]*)`/g, (_, code) => {
        const token = `\uE000CODE${codeSegments.length}\uE001`
        codeSegments.push(code)
        return token
      })

      return escaped
        .replace(/\*\*([^*\n]+)\*\*/g, '<strong>$1</strong>')
        .replace(/\*([^*\n]+)\*/g, '<em>$1</em>')
        .replace(/\r?\n/g, '<br>')
        .replace(/\uE000CODE(\d+)\uE001/g, (_, index) => `<code>${codeSegments[Number(index)]}</code>`)
    }

    const playTTS = async (text) => {
      try {
        // 创建TTS任务
        const createResponse = await api.post('/AI/chat/tts', { text })
        if (createResponse.data && createResponse.data.status_code === 1000 && createResponse.data.task_id) {
          const taskId = createResponse.data.task_id
          
          // 先等待5秒钟再开始轮询
          await new Promise(resolve => setTimeout(resolve, 5000))
          
          // 轮询查询任务结果
          const maxAttempts = 30
          const pollInterval = 2000
          let attempts = 0
          
          const pollResult = async () => {
            const queryResponse = await api.get('/AI/chat/tts/query', { params: { task_id: taskId } })
            
            if (queryResponse.data && queryResponse.data.status_code === 1000) {
              const taskStatus = queryResponse.data.task_status
                
              if (taskStatus === 'Success' && queryResponse.data.task_result) {
                // 任务完成，播放音频
                // 后端返回的 task_result 是直接的 URL 字符串
                const audio = new Audio(queryResponse.data.task_result)
                audio.play()
                return true
              } else if (taskStatus === 'Running' ||taskStatus === 'Created' ) {
                // 任务进行中，继续轮询
                attempts++
                if (attempts < maxAttempts) {
                  await new Promise(resolve => setTimeout(resolve, pollInterval))
                  return await pollResult()
                } else {
                  ElMessage.error('语音合成超时')
                  return true
                }
              } else {
                // 其他状态（如失败）
                ElMessage.error('语音合成失败')
                return true
              }
            }
            
            attempts++
            if (attempts < maxAttempts) {
              await new Promise(resolve => setTimeout(resolve, pollInterval))
              return await pollResult()
            } else {
              ElMessage.error('语音合成超时')
              return true
            }
          }
          
          await pollResult()
        } else {
          ElMessage.error('无法创建语音合成任务')
        }
      } catch (error) {
        console.error('TTS error:', error)
        ElMessage.error('请求语音接口失败')
      }
    }

    const mergeAudioChunks = (chunks) => {
      const length = chunks.reduce((sum, chunk) => sum + chunk.length, 0)
      const merged = new Float32Array(length)
      let offset = 0
      chunks.forEach(chunk => {
        merged.set(chunk, offset)
        offset += chunk.length
      })
      return merged
    }

    const resampleAudio = (samples, inputRate, outputRate = 16000) => {
      if (!samples.length || inputRate === outputRate) return samples
      const outputLength = Math.max(1, Math.round(samples.length * outputRate / inputRate))
      const output = new Float32Array(outputLength)
      const ratio = inputRate / outputRate
      for (let i = 0; i < outputLength; i++) {
        const position = i * ratio
        const left = Math.floor(position)
        const right = Math.min(left + 1, samples.length - 1)
        const fraction = position - left
        output[i] = samples[left] * (1 - fraction) + samples[right] * fraction
      }
      return output
    }

    const encodeWav = (samples, sampleRate = 16000) => {
      const buffer = new ArrayBuffer(44 + samples.length * 2)
      const view = new DataView(buffer)
      const writeText = (offset, text) => {
        for (let i = 0; i < text.length; i++) view.setUint8(offset + i, text.charCodeAt(i))
      }
      writeText(0, 'RIFF')
      view.setUint32(4, 36 + samples.length * 2, true)
      writeText(8, 'WAVE')
      writeText(12, 'fmt ')
      view.setUint32(16, 16, true)
      view.setUint16(20, 1, true)
      view.setUint16(22, 1, true)
      view.setUint32(24, sampleRate, true)
      view.setUint32(28, sampleRate * 2, true)
      view.setUint16(32, 2, true)
      view.setUint16(34, 16, true)
      writeText(36, 'data')
      view.setUint32(40, samples.length * 2, true)
      let offset = 44
      for (let i = 0; i < samples.length; i++, offset += 2) {
        const sample = Math.max(-1, Math.min(1, samples[i]))
        view.setInt16(offset, sample < 0 ? sample * 0x8000 : sample * 0x7fff, true)
      }
      return new Blob([buffer], { type: 'audio/wav' })
    }

    const releaseRecordingResources = async () => {
      if (recordingTimer) clearTimeout(recordingTimer)
      recordingTimer = null
      if (recordingProcessor) {
        recordingProcessor.onaudioprocess = null
        recordingProcessor.disconnect()
      }
      if (recordingSource) recordingSource.disconnect()
      if (recordingSink) recordingSink.disconnect()
      if (recordingStream) recordingStream.getTracks().forEach(track => track.stop())
      if (recordingContext && recordingContext.state !== 'closed') await recordingContext.close()
      recordingProcessor = null
      recordingSource = null
      recordingSink = null
      recordingStream = null
      recordingContext = null
    }

    const startRecording = async () => {
      if (!navigator.mediaDevices?.getUserMedia) {
        ElMessage.error('当前浏览器不支持麦克风录音')
        return
      }
      try {
        recordingChunks = []
        recordingStream = await navigator.mediaDevices.getUserMedia({
          audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true }
        })
        const AudioContextClass = window.AudioContext || window.webkitAudioContext
        recordingContext = new AudioContextClass()
        recordingSource = recordingContext.createMediaStreamSource(recordingStream)
        recordingProcessor = recordingContext.createScriptProcessor(4096, 1, 1)
        recordingSink = recordingContext.createGain()
        recordingSink.gain.value = 0
        recordingProcessor.onaudioprocess = event => {
          recordingChunks.push(new Float32Array(event.inputBuffer.getChannelData(0)))
        }
        recordingSource.connect(recordingProcessor)
        recordingProcessor.connect(recordingSink)
        recordingSink.connect(recordingContext.destination)
        isRecording.value = true
        recordingTimer = setTimeout(() => { void stopRecording(true) }, 55000)
      } catch (error) {
        console.error('Microphone error:', error)
        await releaseRecordingResources()
        ElMessage.error('无法使用麦克风，请检查浏览器权限')
      }
    }

    const stopRecording = async (recognize = true) => {
      if (realtimeVoiceTurnActive.value) return
      if (!isRecording.value && !recordingContext) return
      const inputRate = recordingContext?.sampleRate || 48000
      const chunks = recordingChunks
      recordingChunks = []
      isRecording.value = false
      await releaseRecordingResources()
      if (!recognize) return
      const merged = mergeAudioChunks(chunks)
      if (merged.length < inputRate / 4) {
        ElMessage.warning('录音时间太短，请重新录制')
        return
      }
      transcribing.value = true
      try {
        const samples = resampleAudio(merged, inputRate, 16000)
        const formData = new FormData()
        formData.append('file', encodeWav(samples), 'recording.wav')
        const response = await api.post('/AI/chat/asr', formData)
        if (response.data?.status_code === 1000 && response.data.text) {
          inputMessage.value = inputMessage.value.trim()
            ? `${inputMessage.value.trim()}\n${response.data.text}`
            : response.data.text
          ElMessage.success('语音识别完成')
          await nextTick()
          messageInput.value?.focus()
        } else {
          ElMessage.error(response.data?.status_msg || '语音识别失败')
        }
      } catch (error) {
        console.error('ASR error:', error)
        ElMessage.error('语音识别请求失败')
      } finally {
        transcribing.value = false
      }
    }

    const currentVoiceSessionId = () => {
      if (tempSession.value || !currentSessionId.value || currentSessionId.value === 'temp') return ''
      return String(currentSessionId.value)
    }

    const selectedVoiceProfile = () => {
      const configured = String(process.env.VUE_APP_VOICE_PROFILE || '').trim()
      return configured || 'fish-s2-natural'
    }

    const voiceCSRFToken = () => csrfHeaders()['X-CSRF-Token'] || ''

    const refreshVoiceMessages = () => {
      currentMessages.value = [...currentMessages.value]
      void nextTick().then(() => scrollToBottom())
    }

    const mirrorVoiceMessages = state => {
      if (!state?.sessionId || !sessions.value[state.sessionId]) return
      const session = sessions.value[state.sessionId]
      session.messages = [...currentMessages.value]
      session.historyLoaded = true
      session.historyHasMore = false
      session.historyNextCursor = ''
    }

    const attachVoiceSession = (state, rawSessionId) => {
      const sessionId = String(rawSessionId || '').trim()
      if (!state || !sessionId) return
      state.sessionId = sessionId
      if (!sessions.value[sessionId]) {
        sessions.value[sessionId] = {
          id: sessionId,
          name: '新会话',
          messages: [...currentMessages.value],
          historyLoaded: true,
          historyHasMore: false,
          historyNextCursor: ''
        }
      }
      currentSessionId.value = sessionId
      tempSession.value = false
      mirrorVoiceMessages(state)
    }

    const appendVoiceUserMessage = (state, transcript) => {
      if (!state || state.userMessage || !transcript) return
      state.userMessage = {
        key: nextMessageKey(),
        role: 'user',
        content: transcript
      }
      currentMessages.value.push(state.userMessage)
      mirrorVoiceMessages(state)
      refreshVoiceMessages()
    }

    const ensureVoiceAssistantMessage = state => {
      if (!state) return null
      if (!state.assistantMessage) {
        state.assistantMessage = {
          key: nextMessageKey(),
          role: 'assistant',
          content: '',
          citations: [],
          meta: { status: 'streaming' }
        }
        currentMessages.value.push(state.assistantMessage)
      }
      return state.assistantMessage
    }

    const releaseRealtimeVoiceTurn = async (client, { stopPlayback = true } = {}) => {
      if (voiceTurnClient !== client) return
      if (voiceTurnState) voiceTurnState.closing = true
      voiceTurnClient = null
      voiceTurnState = null
      isRecording.value = false
      transcribing.value = false
      loading.value = false
      realtimeVoiceTurnActive.value = false
      await client.close({ stopPlayback, notifyServer: true })
    }

    const handleRealtimeVoiceError = (error, client) => {
      const state = voiceTurnState
      if (voiceTurnClient !== client || !state || state.closing || !state.ready || state.failed) return
      state.failed = true
      if (state.assistantMessage) {
        state.assistantMessage.meta = { status: 'error' }
        if (!state.assistantMessage.content) state.assistantMessage.content = '[语音对话出错，请重试]'
        refreshVoiceMessages()
      }
      const message = error instanceof VoiceTurnError ? error.message : '实时语音连接发生错误。'
      ElMessage.error(message)
      announceChat('语音对话失败，请重试')
      void releaseRealtimeVoiceTurn(client, { stopPlayback: true })
    }

    const handleRealtimeVoiceEvent = (event, client) => {
      const state = voiceTurnState
      if (voiceTurnClient !== client || !state || state.closing) return
      if (event.turnId && event.turnId !== state.turnId) return

      switch (event.type) {
        case 'ready':
          state.ready = true
          announceChat('实时语音已就绪，开始说话后再次点击麦克风即可发送')
          break
        case 'vad.speech_started':
          announceChat('检测到语音')
          break
        case 'vad.speech_stopped':
          announceChat('检测到停顿；点击停止后发送本次语音')
          break
        case 'turn.processing':
          isRecording.value = false
          transcribing.value = true
          loading.value = true
          announceChat('正在识别语音并生成回复')
          break
        case 'asr.final': {
          const transcript = typeof event.text === 'string' ? event.text.trim() : ''
          if (transcript) appendVoiceUserMessage(state, transcript)
          break
        }
        case 'session.created':
          attachVoiceSession(state, event.sessionId)
          break
        case 'assistant.delta': {
          const message = ensureVoiceAssistantMessage(state)
          if (message && typeof event.text === 'string') {
            message.content += event.text
            mirrorVoiceMessages(state)
            refreshVoiceMessages()
          }
          break
        }
        case 'assistant.citations': {
          const message = ensureVoiceAssistantMessage(state)
          if (message) {
            message.citations = normalizeCitations(event.citations)
            mirrorVoiceMessages(state)
            refreshVoiceMessages()
          }
          break
        }
        case 'tts.unavailable':
          if (!state.ttsWarningShown) {
            state.ttsWarningShown = true
            ElMessage.warning(event.message || '实时语音合成暂不可用，已保留文本回复。')
          }
          break
        case 'tts.error':
          ElMessage.warning(event.message || '有一段语音合成失败，文本回复不受影响。')
          break
        case 'turn.cancelled':
          if (state.assistantMessage) {
            state.assistantMessage.meta = { status: 'cancelled' }
            if (!state.assistantMessage.content) state.assistantMessage.content = '[已停止语音回复]'
            refreshVoiceMessages()
          }
          announceChat('已停止语音对话')
          void releaseRealtimeVoiceTurn(client, { stopPlayback: true })
          break
        case 'turn.completed':
          if (event.sessionId) attachVoiceSession(state, event.sessionId)
          if (state.assistantMessage) state.assistantMessage.meta = { status: 'done' }
          mirrorVoiceMessages(state)
          refreshVoiceMessages()
          announceChat('语音对话已完成')
          void releaseRealtimeVoiceTurn(client, { stopPlayback: false })
          break
        case 'error':
          // VoiceTurnClient routes this to onError first. This branch keeps
          // the event visible to callers without showing a duplicate toast.
          break
        default:
          break
      }
    }

    const startRealtimeVoiceTurn = async () => {
      const turnId = createTurnID()
      const sessionId = currentVoiceSessionId()
      const state = {
        turnId,
        sessionId,
        ready: false,
        closing: false,
        failed: false,
        userMessage: null,
        assistantMessage: null,
        ttsWarningShown: false
      }
      const client = new VoiceTurnClient({
        url: realtimeVoiceURL(buildApiUrl('/AI/voice/realtime')),
        onEvent: event => handleRealtimeVoiceEvent(event, client),
        onError: error => handleRealtimeVoiceError(error, client),
        onClose: event => {
          const activeState = voiceTurnState
          if (voiceTurnClient !== client || !activeState || activeState.closing || disposed) return
          if (activeState.ready) {
            handleRealtimeVoiceError(new VoiceTurnError('socket_closed', `实时语音连接已关闭（${event.code}）。`), client)
          }
        }
      })
      voiceTurnClient = client
      voiceTurnState = state
      realtimeVoiceTurnActive.value = true

      try {
        await client.start({
          turnId,
          sessionId,
          ...buildChatSelection(),
          voiceProfileId: selectedVoiceProfile(),
          csrfToken: voiceCSRFToken()
        })
        if (voiceTurnClient !== client || disposed) return
        state.ready = true
        isRecording.value = true
        announceChat('正在录制实时语音；点击麦克风发送')
      } catch (error) {
        if (voiceTurnClient === client) {
          voiceTurnClient = null
          voiceTurnState = null
          realtimeVoiceTurnActive.value = false
          isRecording.value = false
          transcribing.value = false
          loading.value = false
        }
        const causeName = error?.cause?.name || error?.name
        const permissionDenied = causeName === 'NotAllowedError' || causeName === 'NotFoundError'
        if (!permissionDenied && error?.code !== 'invalid_start') {
          ElMessage.warning('实时语音暂不可用，已切换到兼容录音模式')
          await startRecording()
          return
        }
        console.error('Realtime voice start error:', error)
        ElMessage.error('无法使用麦克风，请检查浏览器权限')
      }
    }

    const commitRealtimeVoiceTurn = async () => {
      const client = voiceTurnClient
      if (!client) return
      isRecording.value = false
      transcribing.value = true
      loading.value = true
      announceChat('正在发送语音')
      try {
        await client.commit()
      } catch (error) {
        handleRealtimeVoiceError(error, client)
      }
    }

    const interruptRealtimeVoiceTurn = async ({ silent = false } = {}) => {
      const client = voiceTurnClient
      if (!client) return
      const state = voiceTurnState
      if (state) state.closing = true
      if (state?.assistantMessage) {
        state.assistantMessage.meta = { status: 'cancelled' }
        if (!state.assistantMessage.content) state.assistantMessage.content = '[已停止语音回复]'
        refreshVoiceMessages()
      }
      try {
        await client.interrupt()
      } finally {
        if (voiceTurnClient === client) {
          voiceTurnClient = null
          voiceTurnState = null
          isRecording.value = false
          transcribing.value = false
          loading.value = false
          realtimeVoiceTurnActive.value = false
        }
      }
      if (!silent) {
        announceChat('已停止语音对话')
        ElMessage.info('已停止语音对话')
      }
    }

    const toggleRecording = async () => {
      if (realtimeVoiceTurnActive.value) {
        if (isRecording.value) await commitRealtimeVoiceTurn()
        else await interruptRealtimeVoiceTurn()
        return
      }
      if (transcribing.value || loading.value) return
      if (isRecording.value) await stopRecording(true)
      else await startRealtimeVoiceTurn()
    }

    const loadSessions = async ({ append = false } = {}) => {
      if (sessionsRequestPromise) return sessionsRequestPromise
      const cursor = append ? sessionsNextCursor.value : ''
      if (append && (!sessionsHasMore.value || !cursor)) return false

      const requestSerial = ++sessionsRequestSerial
      const controller = new AbortController()
      sessionsAbortController = controller
      if (append) {
        sessionsLoadingMore.value = true
        sessionsMoreError.value = ''
      } else {
        sessionsLoading.value = true
        sessionsError.value = ''
        sessionsMoreError.value = ''
        sessionsHasMore.value = false
        sessionsNextCursor.value = ''
      }
      let request
      request = (async () => {
        try {
          const params = { limit: sessionPageSize }
          if (append) params.cursor = cursor
          const response = await api.get('/AI/chat/sessions', { signal: controller.signal, params })
          if (response.data?.status_code !== 1000 || !Array.isArray(response.data.sessions)) {
            throw new Error(response.data?.status_msg || '会话列表格式无效')
          }
          if (disposed || requestSerial !== sessionsRequestSerial) return null

          const sessionMap = append ? { ...sessions.value } : {}
          response.data.sessions.forEach(session => {
            const sessionID = session.sessionId ?? session.session_id ?? session.id
            if (sessionID === null || sessionID === undefined || sessionID === '') return
            const sid = String(sessionID)
            const previous = sessions.value[sid]
            sessionMap[sid] = {
              id: sid,
              name: session.name || `会话 ${sid}`,
              messages: Array.isArray(previous?.messages) ? previous.messages : [],
              historyLoaded: Boolean(previous?.historyLoaded),
              historyHasMore: Boolean(previous?.historyHasMore),
              historyNextCursor: previous?.historyNextCursor || ''
            }
          })
          const pagination = paginationFrom(response.data)
          sessions.value = sessionMap
          sessionsHasMore.value = pagination.hasMore
          sessionsNextCursor.value = pagination.hasMore ? pagination.nextCursor : ''
          if (!tempSession.value && currentSessionId.value && !sessionMap[currentSessionId.value]) {
            currentSessionId.value = null
            currentMessages.value = []
            sessionHistoryError.value = '当前会话已不存在，请选择其他会话或新建聊天。'
          }
          return true
        } catch (error) {
          if (isAbortError(error) || disposed || requestSerial !== sessionsRequestSerial) return null
          if (append) sessionsMoreError.value = error.message || '加载更多会话失败，请重试'
          else sessionsError.value = error.message || '加载会话列表失败，请重试'
          return false
        } finally {
          if (requestSerial === sessionsRequestSerial) {
            if (append) sessionsLoadingMore.value = false
            else sessionsLoading.value = false
            sessionsAbortController = null
          }
          if (sessionsRequestPromise === request) sessionsRequestPromise = null
        }
      })()
      sessionsRequestPromise = request
      return request
    }

    const retryLoadSessions = () => loadSessions()
    const loadMoreSessions = () => loadSessions({ append: true })

    const toChatMessages = history => history.map(item => ({
      key: nextMessageKey(item),
      role: item.is_user ? 'user' : 'assistant',
      content: item.content,
      citations: item.is_user ? [] : normalizeCitations(item.citations ?? item.references)
    }))

    const loadSessionHistory = async (sessionId, { force = false } = {}) => {
      const sid = String(sessionId || '')
      const targetSession = sessions.value[sid]
      if (!sid || !targetSession) return null
      if (!force && targetSession.historyLoaded) return true
      if (historyRequestPromise) {
        if (historyRequestSessionId === sid) return historyRequestPromise
        historyAbortController?.abort()
        await historyRequestPromise
      }

      const requestSerial = ++historyRequestSerial
      const controller = new AbortController()
      historyAbortController = controller
      historyRequestSessionId = sid
      sessionHistoryLoading.value = true
      sessionHistoryError.value = ''
      historyMoreError.value = ''
      let request
      request = (async () => {
        try {
          const response = await api.post('/AI/chat/history', {
            sessionId: sid,
            limit: historyPageSize
          }, { signal: controller.signal })
          if (response.data?.status_code !== 1000 || !Array.isArray(response.data.history)) {
            throw new Error(response.data?.status_msg || '无法获取历史数据')
          }
          if (disposed || currentSessionId.value !== sid || requestSerial !== historyRequestSerial) return null
          const current = sessions.value[sid]
          if (!current) return null
          const messages = toChatMessages(response.data.history)
          const pagination = paginationFrom(response.data)
          current.messages = messages
          current.historyLoaded = true
          current.historyHasMore = pagination.hasMore
          current.historyNextCursor = pagination.hasMore ? pagination.nextCursor : ''
          currentMessages.value = [...messages]
          await nextTick()
          scrollToBottom()
          return true
        } catch (error) {
          if (isAbortError(error) || disposed || currentSessionId.value !== sid || requestSerial !== historyRequestSerial) return null
          sessionHistoryError.value = error.message || '加载会话历史失败，请重试'
          return false
        } finally {
          if (requestSerial === historyRequestSerial) {
            sessionHistoryLoading.value = false
            historyAbortController = null
            historyRequestSessionId = ''
          }
          if (historyRequestPromise === request) historyRequestPromise = null
        }
      })()
      historyRequestPromise = request
      return request
    }

    const loadMoreSessionHistory = async () => {
      const sid = String(currentSessionId.value || '')
      const targetSession = sessions.value[sid]
      const cursor = targetSession?.historyNextCursor || ''
      if (tempSession.value || loading.value || !sid || !targetSession || !targetSession.historyHasMore || !cursor) return false
      if (historyRequestPromise) {
        if (historyRequestSessionId === sid) return historyRequestPromise
        historyAbortController?.abort()
        await historyRequestPromise
      }

      const scrollContainer = messagesRef.value
      const previousScrollTop = scrollContainer?.scrollTop || 0
      const previousScrollHeight = scrollContainer?.scrollHeight || 0
      const requestSerial = ++historyRequestSerial
      const controller = new AbortController()
      historyAbortController = controller
      historyRequestSessionId = sid
      historyLoadingMore.value = true
      historyMoreError.value = ''
      let request
      request = (async () => {
        try {
          const response = await api.post('/AI/chat/history', {
            sessionId: sid,
            limit: historyPageSize,
            cursor
          }, { signal: controller.signal })
          if (response.data?.status_code !== 1000 || !Array.isArray(response.data.history)) {
            throw new Error(response.data?.status_msg || '无法获取更早消息')
          }
          if (disposed || currentSessionId.value !== sid || requestSerial !== historyRequestSerial) return null
          const current = sessions.value[sid]
          if (!current) return null
          const olderMessages = toChatMessages(response.data.history)
          const knownKeys = new Set(current.messages.map(message => message.key).filter(Boolean))
          const uniqueOlderMessages = olderMessages.filter(message => !knownKeys.has(message.key))
          const pagination = paginationFrom(response.data)
          current.messages = [...uniqueOlderMessages, ...current.messages]
          current.historyHasMore = pagination.hasMore
          current.historyNextCursor = pagination.hasMore ? pagination.nextCursor : ''
          currentMessages.value = [...current.messages]
          await nextTick()
          if (messagesRef.value === scrollContainer && scrollContainer) {
            scrollContainer.scrollTop = previousScrollTop + (scrollContainer.scrollHeight - previousScrollHeight)
          }
          return true
        } catch (error) {
          if (isAbortError(error) || disposed || currentSessionId.value !== sid || requestSerial !== historyRequestSerial) return null
          historyMoreError.value = error.message || '加载更早消息失败，请重试'
          return false
        } finally {
          if (requestSerial === historyRequestSerial) {
            historyLoadingMore.value = false
            historyAbortController = null
            historyRequestSessionId = ''
          }
          if (historyRequestPromise === request) historyRequestPromise = null
        }
      })()
      historyRequestPromise = request
      return request
    }

    const loadModels = async () => {
      modelsLoading.value = true
      try {
        const response = await api.get('/AI/models')
        if (response.data?.status_code !== 1000 || !Array.isArray(response.data.models)) return
        const available = response.data.models
          .filter(model => model.available !== false)
          .map(model => ({
            ...model,
            id: String(model.id),
            displayName: model.displayName || model.name || String(model.id),
            aliases: Array.isArray(model.aliases) ? model.aliases.map(String) : []
          }))
        if (!available.length) return
        modelOptions.value = available
        const selected = available.find(model => model.id === selectedModel.value || (model.aliases || []).includes(selectedModel.value))
        selectedModel.value = selected?.id || available[0].id
      } catch (error) {
        console.warn('Model catalog unavailable, using compatibility list:', error)
      } finally {
        modelsLoading.value = false
      }
    }

    const loadKnowledgeBases = async () => {
      if (knowledgeBasesLoading.value || knowledgeBasesLoaded.value) return
      knowledgeBasesLoading.value = true
      try {
        const response = await api.get('/file/knowledge-bases', { params: { page: 1, page_size: 100 } })
        if (response.data?.status_code !== 1000 || !Array.isArray(response.data.knowledge_bases)) {
          throw new Error(response.data?.status_msg || '获取知识库失败')
        }
        knowledgeBases.value = response.data.knowledge_bases.map(base => {
          const name = base.name || `知识库 ${base.id}`
          const documentCount = numberOrNull(base.document_count ?? base.documentCount)
          return {
            ...base,
            id: String(base.id),
            name,
            optionLabel: documentCount === null ? name : `${name}（${documentCount} 个文档）`
          }
        })
        if (selectedKnowledgeBaseId.value && !knowledgeBases.value.some(base => base.id === selectedKnowledgeBaseId.value)) {
          selectedKnowledgeBaseId.value = ''
        }
        knowledgeBasesLoaded.value = true
      } catch (error) {
        console.error('Load knowledge bases error:', error)
        ElMessage.error(error.message || '获取知识库失败')
      } finally {
        knowledgeBasesLoading.value = false
      }
    }

    const cancelQueuedStreamRender = () => {
      if (streamRenderFrame !== null) {
        if (typeof window.cancelAnimationFrame === 'function') window.cancelAnimationFrame(streamRenderFrame)
        else window.clearTimeout(streamRenderFrame)
      }
      streamRenderFrame = null
      const resolve = resolveStreamRefresh
      resolveStreamRefresh = null
      streamRefreshPromise = null
      if (resolve) resolve()
    }

    const queueStreamRefresh = aiMessage => {
      if (disposed || !currentMessages.value.includes(aiMessage)) return Promise.resolve()
      if (streamRefreshPromise) return streamRefreshPromise
      streamRefreshPromise = new Promise(resolve => {
        resolveStreamRefresh = resolve
        const flush = async () => {
          streamRenderFrame = null
          try {
            if (!disposed && currentMessages.value.includes(aiMessage)) {
              currentMessages.value = [...currentMessages.value]
              await nextTick()
              scrollToBottom()
            }
          } finally {
            const done = resolveStreamRefresh
            resolveStreamRefresh = null
            streamRefreshPromise = null
            if (done) done()
          }
        }
        streamRenderFrame = typeof window.requestAnimationFrame === 'function'
          ? window.requestAnimationFrame(flush)
          : window.setTimeout(flush, 16)
      })
      return streamRefreshPromise
    }

    const stopStreaming = () => {
      if (activeStreamController) {
        activeStreamController.abort()
      }
    }

    const createNewSession = () => {
      if (realtimeVoiceTurnActive.value || (loading.value && !activeStreamController)) {
        ElMessage.info('请等待当前消息发送完成')
        return
      }
      stopStreaming()
      historyAbortController?.abort()
      currentSessionId.value = 'temp'
      tempSession.value = true
      currentMessages.value = []
      sessionHistoryLoading.value = false
      sessionHistoryError.value = ''
      historyLoadingMore.value = false
      historyMoreError.value = ''
      // focus input
      nextTick(() => {
        if (messageInput.value) messageInput.value.focus()
      })
    }

    const switchSession = async (sessionId) => {
      if (!sessionId) return
      if (realtimeVoiceTurnActive.value || (loading.value && !activeStreamController)) {
        ElMessage.info('请等待当前消息发送完成')
        return
      }
      const sid = String(sessionId)
      const targetSession = sessions.value[sid]
      if (!targetSession) {
        ElMessage.error('会话不存在，请刷新后重试')
        return
      }

      stopStreaming()
      if (historyRequestPromise && historyRequestSessionId !== sid) historyAbortController?.abort()
      currentSessionId.value = sid
      tempSession.value = false
      sessionHistoryError.value = ''
      historyMoreError.value = ''
      currentMessages.value = [...(targetSession.messages || [])]

      if (!targetSession.historyLoaded) await loadSessionHistory(sid)
      await nextTick()
      scrollToBottom()
    }

    const syncHistory = async () => {
      if (realtimeVoiceTurnActive.value || !currentSessionId.value || tempSession.value) {
        ElMessage.warning('请选择已有会话进行同步')
        return
      }
      await loadSessionHistory(currentSessionId.value, { force: true })
    }

    const retryCurrentSessionHistory = () => {
      if (!currentSessionId.value || tempSession.value) return
      return loadSessionHistory(currentSessionId.value, { force: true })
    }


    const sendMessage = async () => {
      if (loading.value || realtimeVoiceTurnActive.value || sessionHistoryLoading.value || historyLoadingMore.value) return

      if (!inputMessage.value || !inputMessage.value.trim()) {
        ElMessage.warning('请输入消息内容')
        return
      }

      if (!currentSessionId.value || (!tempSession.value && !sessions.value[currentSessionId.value])) {
        createNewSession()
      }

      const userMessage = {
        key: nextMessageKey(),
        role: 'user',
        content: inputMessage.value
      }
      const currentInput = inputMessage.value
      inputMessage.value = ''
      loading.value = true

      currentMessages.value.push(userMessage)
      announceChat('消息已发送，正在等待 AI 回复')

      try {
        await nextTick()
        scrollToBottom()
        if (isStreaming.value) {

          await handleStreaming(currentInput)
        } else {

          await handleNormal(currentInput)
        }
      } catch (err) {
        console.error('Send message error:', err)
        ElMessage.error('发送失败，请重试')
        announceChat('消息发送失败，请重试')

        if (!tempSession.value && currentSessionId.value && sessions.value[currentSessionId.value] && sessions.value[currentSessionId.value].messages) {

          const sessionArr = sessions.value[currentSessionId.value].messages
          if (sessionArr && sessionArr.length) sessionArr.pop()
        }
        currentMessages.value.pop()
      } finally {
        loading.value = false
        await nextTick()
        scrollToBottom()
      }
    }


    async function handleStreaming(question) {
      const isNewSession = tempSession.value
      const requestSessionId = isNewSession ? null : String(currentSessionId.value)
      const aiMessage = {
        key: nextMessageKey(),
        role: 'assistant',
        content: '',
        citations: [],
        meta: { status: 'streaming' }
      }

      currentMessages.value.push(aiMessage)
      announceChat('AI 正在生成回复')

      if (!isNewSession && sessions.value[requestSessionId]) {
        if (!sessions.value[requestSessionId].messages) {
          sessions.value[requestSessionId].messages = []
        }
        sessions.value[requestSessionId].historyLoaded = true
        sessions.value[requestSessionId].messages.push(
          { key: nextMessageKey(), role: 'user', content: question },
          aiMessage
        )
      }

      const url = buildApiUrl(isNewSession
        ? '/AI/chat/send-stream-new-session'
        : '/AI/chat/send-stream')

      const headers = {
        'Content-Type': 'application/json',
        'Accept': 'text/event-stream',
        ...csrfHeaders()
      }

      const body = {
        question,
        ...buildChatSelection(),
        ...(isNewSession ? {} : { sessionId: requestSessionId })
      }

      const controller = new AbortController()
      activeStreamController = controller

      const refreshMessage = () => queueStreamRefresh(aiMessage)

      const appendContent = content => {
        aiMessage.content += content
        void refreshMessage()
      }

      const replaceCitations = citations => {
        aiMessage.citations = normalizeCitations(citations)
        void refreshMessage()
      }

      const attachNewSession = (sessionId) => {
        if (!isNewSession || !sessionId || !currentMessages.value.includes(aiMessage)) return
        const sid = String(sessionId)
        sessions.value[sid] = {
          id: sid,
          name: '新会话',
          messages: [...currentMessages.value],
          historyLoaded: true,
          historyHasMore: false,
          historyNextCursor: ''
        }
        currentSessionId.value = sid
        tempSession.value = false
      }

      const processSseData = async (data) => {
        if (data === '[DONE]') return true

        if (data.trimStart().startsWith('{')) {
          let parsed = null
          try {
            parsed = JSON.parse(data.trim())
          } catch {
            // 不是 JSON，继续按普通文本处理
          }
          if (parsed) {
            if (parsed.sessionId) attachNewSession(parsed.sessionId)

            const streamedContent = parsed.content ?? parsed.delta ?? parsed.text
            if (typeof streamedContent === 'string') {
              await appendContent(streamedContent)
            }
            if (Object.prototype.hasOwnProperty.call(parsed, 'citations') || Object.prototype.hasOwnProperty.call(parsed, 'references')) {
              await replaceCitations(parsed.citations ?? parsed.references)
            }
            if (parsed.error || (parsed.message && typeof streamedContent !== 'string')) {
              throw new Error(parsed.error || parsed.message)
            }
            return false
          }
        }

        await appendContent(data)
        return false
      }

      const processSseLine = async (rawLine) => {
        const line = rawLine.endsWith('\r') ? rawLine.slice(0, -1) : rawLine
        if (!line || !line.startsWith('data:')) return false
        let data = line.slice(5)
        if (data.startsWith(' ')) data = data.slice(1)
        return processSseData(data)
      }

      try {
        const response = await fetch(url, {
          method: 'POST',
          headers,
          body: JSON.stringify(body),
          signal: controller.signal,
          credentials: 'include'
        })

        if (response.status === 401) {
          handleUnauthorized()
          throw new Error('Unauthorized')
        }
        if (!response.ok) {
          throw new Error(`Stream request failed with status ${response.status}`)
        }
        const contentType = response.headers.get('content-type') || ''
        if (!contentType.includes('text/event-stream')) {
          const payload = await response.json().catch(() => null)
          if (isAuthFailure(payload)) handleUnauthorized()
          throw new Error(payload?.status_msg || 'Stream endpoint did not return SSE')
        }
        if (!response.body) throw new Error('ReadableStream is not supported')

        const reader = response.body.getReader()
        const decoder = new TextDecoder()
        let buffer = ''
        let streamFinished = false

        while (!streamFinished) {
          const { done, value } = await reader.read()
          if (done) break

          buffer += decoder.decode(value, { stream: true })
          const lines = buffer.split('\n')
          buffer = lines.pop() || ''

          for (const line of lines) {
            streamFinished = await processSseLine(line)
            if (streamFinished) break
          }
        }

        buffer += decoder.decode()
        if (!streamFinished && buffer) {
          streamFinished = await processSseLine(buffer)
        }

        if (!streamFinished) throw new Error('流式响应在完成标记前中断')
        await reader.cancel()
        aiMessage.meta = { status: 'done' }
        await refreshMessage()
        announceChat('AI 回复已完成')
      } catch (err) {
        if (err.name === 'AbortError') {
          aiMessage.meta = { status: 'cancelled' }
          if (!aiMessage.content) aiMessage.content = '[已停止生成]'
          if (currentMessages.value.includes(aiMessage)) announceChat('已停止生成')
        } else {
          console.error('Stream error:', err)
          aiMessage.meta = { status: 'error' }
          if (!aiMessage.content) aiMessage.content = '[错误] 流式传输出错，请重试'
          ElMessage.error('流式传输出错')
          if (currentMessages.value.includes(aiMessage)) announceChat('AI 回复生成失败，请重试')
        }
        await refreshMessage()
      } finally {
        if (activeStreamController === controller) {
          activeStreamController = null
        }
      }
    }


    async function handleNormal(question) {
      if (tempSession.value) {

        const response = await api.post('/AI/chat/send-new-session', {
          question,
          ...buildChatSelection()
        }, { timeout: normalChatTimeout })
        if (response.data && response.data.status_code === 1000) {
          const sessionId = String(response.data.sessionId)
          const aiMessage = {
            key: nextMessageKey(),
            role: 'assistant',
            content: responseContent(response.data),
            citations: normalizeCitations(response.data.citations ?? response.data.references)
          }

          sessions.value[sessionId] = {
            id: sessionId,
            name: '新会话',
            messages: [ { key: nextMessageKey(), role: 'user', content: question }, aiMessage ],
            historyLoaded: true,
            historyHasMore: false,
            historyNextCursor: ''
          }
          currentSessionId.value = sessionId
          tempSession.value = false
          currentMessages.value = [...sessions.value[sessionId].messages]
          announceChat('AI 回复已完成')
        } else {
          ElMessage.error(response.data?.status_msg || '发送失败')

          currentMessages.value.pop()
        }
      } else {

        const sessionMsgs = sessions.value[currentSessionId.value].messages

        sessionMsgs.push({ key: nextMessageKey(), role: 'user', content: question })

        const response = await api.post('/AI/chat/send', {
          question,
          ...buildChatSelection(),
          sessionId: currentSessionId.value
        }, { timeout: normalChatTimeout })
        if (response.data && response.data.status_code === 1000) {
          const aiMessage = {
            key: nextMessageKey(),
            role: 'assistant',
            content: responseContent(response.data),
            citations: normalizeCitations(response.data.citations ?? response.data.references)
          }
          sessionMsgs.push(aiMessage)
          currentMessages.value = [...sessionMsgs]
          sessions.value[currentSessionId.value].historyLoaded = true
          announceChat('AI 回复已完成')
        } else {
          ElMessage.error(response.data?.status_msg || '发送失败')
          sessionMsgs.pop() // rollback
          currentMessages.value.pop()
        }
      }
    }


    const scrollToBottom = () => {
      if (messagesRef.value) {
        try {
          messagesRef.value.scrollTop = messagesRef.value.scrollHeight
        } catch (e) {
          // ignore
        }
      }
    }

    const triggerFileUpload = () => {
      if (fileInput.value) {
        fileInput.value.click()
      }
    }

    const handleFileUpload = async (event) => {
      const file = event.target.files[0]
      if (!file) return

      // 前端校验：只允许.md或.txt文件
      const fileName = file.name.toLowerCase()
      if (!fileName.endsWith('.md') && !fileName.endsWith('.txt')) {
        ElMessage.error('只允许上传 .md 或 .txt 文件')
        // 清空文件输入
        if (fileInput.value) {
          fileInput.value.value = ''
        }
        return
      }

      try {
        uploading.value = true
        const formData = new FormData()
        formData.append('file', file)

        const response = await api.post('/file/upload', formData)

        if (response.data && response.data.status_code === 1000) {
          ElMessage.success(`文件上传成功`)
        } else {
          ElMessage.error(response.data?.status_msg || '上传失败')
        }
      } catch (error) {
        console.error('File upload error:', error)
        ElMessage.error('文件上传失败')
      } finally {
        uploading.value = false
        // 清空文件输入
        if (fileInput.value) {
          fileInput.value.value = ''
        }
      }
    }

    watch(isRagPipeline, enabled => {
      if (enabled) void loadKnowledgeBases()
    })

    onMounted(() => {
      void loadSessions()
      void loadModels()
    })

    onBeforeUnmount(() => {
      disposed = true
      stopStreaming()
      sessionsAbortController?.abort()
      historyAbortController?.abort()
      cancelQueuedStreamRender()
      if (announcementTimer !== null) window.clearTimeout(announcementTimer)
      void interruptRealtimeVoiceTurn({ silent: true })
      void stopRecording(false)
    })

    // expose to template
    return {
      sessions: computed(() => Object.values(sessions.value)),
      sessionsLoading,
      sessionsError,
      sessionsHasMore,
      sessionsLoadingMore,
      sessionsMoreError,
      currentSessionId,
      tempSession,
      currentMessages,
      sessionHistoryLoading,
      sessionHistoryError,
      currentSessionHasMore,
      historyLoadingMore,
      historyMoreError,
      chatAnnouncement,
      inputMessage,
      loading,
      messagesRef,
      messageInput,
      selectedModel,
      modelsLoading,
      modelOptions,
      isRagPipeline,
      knowledgeBases,
      knowledgeBasesLoading,
      selectedKnowledgeBaseId,
      isStreaming,
      uploading,
      fileInput,
      isRecording,
      transcribing,
      realtimeVoiceTurnActive,
      renderMarkdown,
      playTTS,
      stopStreaming,
      createNewSession,
      switchSession,
      syncHistory,
      retryLoadSessions,
      loadMoreSessions,
      retryCurrentSessionHistory,
      loadMoreSessionHistory,
      sendMessage,
      triggerFileUpload,
      handleFileUpload,
      toggleRecording
    }
  }
}
</script>

<style scoped>
/* ==========================================================================
   AI Chat — a two-pane document window.
   Left: frosted conversation sidebar. Right: toolbar, transcript, composer.
   Colour is greyscale only; state is carried by fill weight, never by hue.
   ========================================================================== */

.ai-chat-container {
  display: flex;
  height: 100vh;
  overflow: hidden;
  color: var(--mac-text);
  background: var(--mac-bg);
}

/* ---- Sidebar ------------------------------------------------------------ */
/*
 * The most "native" surface in the app: vibrancy sampling the canvas behind
 * it, a hairline divider instead of a shadow, and rows that select with a
 * quiet fill rather than a highlight colour.
 */

.session-list {
  z-index: 2;
  display: flex;
  flex: 0 0 264px;
  flex-direction: column;
  width: 264px;
  height: 100vh;
  overflow: hidden;
  border-right: 1px solid var(--mac-border-soft);
  background: var(--mac-material-strong);
  -webkit-backdrop-filter: var(--mac-material-blur);
  backdrop-filter: var(--mac-material-blur);
}

.session-list-header {
  display: flex;
  flex-direction: column;
  align-items: stretch;
  gap: var(--mac-space-3);
  padding: 18px 12px 12px;
}

.session-list-header > span {
  padding: 0 4px;
  color: var(--mac-text-secondary);
  font-size: var(--mac-text-base);
  font-weight: 650;
  letter-spacing: var(--mac-tracking-tight);
}

.new-chat-btn {
  width: 100%;
  min-height: 32px;
  padding: 7px 12px;
  border: 1px solid transparent;
  border-radius: var(--mac-radius-control);
  color: var(--mac-text-inverse);
  background: var(--mac-black);
  font-size: var(--mac-text-base);
  font-weight: 590;
  transition: background var(--mac-dur-fast) var(--mac-ease);
}

.new-chat-btn:hover {
  background: var(--mac-black-hover);
}

.new-chat-btn:active {
  background: var(--mac-black-active);
}

.session-list-ul {
  flex: 1;
  margin: 0;
  padding: 0 8px 12px;
  overflow-y: auto;
  list-style: none;
}

.session-item {
  padding: 8px 10px;
  overflow: hidden;
  border-radius: var(--mac-radius-md);
  color: var(--mac-text-secondary);
  font-size: var(--mac-text-base);
  text-overflow: ellipsis;
  white-space: nowrap;
  cursor: pointer;
  transition: background var(--mac-dur-fast) var(--mac-ease),
    color var(--mac-dur-fast) var(--mac-ease);
}

.session-item + .session-item {
  margin-top: 2px;
}

.session-item:hover {
  color: var(--mac-text);
  background: var(--mac-surface-muted);
}

.session-item.active {
  color: var(--mac-text);
  background: var(--mac-surface-strong);
  font-weight: 590;
}

.session-list-state,
.session-list-error {
  margin: 0 12px 12px;
  padding: 10px 12px;
  border-radius: var(--mac-radius-md);
  color: var(--mac-text-tertiary);
  background: var(--mac-surface-muted);
  font-size: var(--mac-text-base);
}

/* Failure reads as an unavailable state: dashed outline, no second hue. */
.session-list-error {
  display: grid;
  gap: var(--mac-space-2);
  border: 1px dashed var(--mac-border-strong);
  color: var(--mac-text-secondary);
  background: var(--mac-surface);
}

.session-list-more-error {
  margin-bottom: var(--mac-space-2);
}

.session-retry-btn,
.history-retry-btn,
.load-more-sessions-btn {
  min-height: 28px;
  padding: 5px 10px;
  border: 1px solid var(--mac-border);
  border-radius: var(--mac-radius-control);
  color: var(--mac-text-secondary);
  background: var(--mac-surface);
  font: inherit;
  font-size: var(--mac-text-base);
  cursor: pointer;
  transition: background var(--mac-dur-fast) var(--mac-ease),
    border-color var(--mac-dur-fast) var(--mac-ease),
    color var(--mac-dur-fast) var(--mac-ease);
}

.session-retry-btn:hover:not(:disabled),
.history-retry-btn:hover:not(:disabled),
.load-more-sessions-btn:hover:not(:disabled) {
  border-color: var(--mac-border-strong);
  color: var(--mac-text);
  background: var(--mac-surface-muted);
}

.session-retry-btn:disabled,
.history-retry-btn:disabled,
.load-more-sessions-btn:disabled {
  color: var(--mac-text-quaternary);
  background: var(--mac-surface-muted);
  cursor: default;
}

.session-retry-btn {
  width: fit-content;
}

.load-more-sessions-btn {
  margin: 0 12px 12px;
}

/* ---- Chat column -------------------------------------------------------- */

.chat-section {
  display: flex;
  flex: 1;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
  overflow: hidden;
}

.top-bar {
  z-index: 5;
  display: flex;
  align-items: center;
  gap: var(--mac-space-2);
  min-height: 60px;
  padding: 10px clamp(16px, 3vw, 28px);
  border-bottom: 1px solid var(--mac-border-soft);
  color: var(--mac-text);
  background: var(--mac-material-strong);
  -webkit-backdrop-filter: var(--mac-material-blur);
  backdrop-filter: var(--mac-material-blur);
}

.back-btn {
  min-height: 32px;
  padding: 6px 10px;
  border: 1px solid transparent;
  border-radius: var(--mac-radius-control);
  color: var(--mac-text-secondary);
  background: transparent;
  font-size: var(--mac-text-base);
  cursor: pointer;
  transition: background var(--mac-dur-fast) var(--mac-ease),
    color var(--mac-dur-fast) var(--mac-ease);
}

.back-btn:hover {
  color: var(--mac-text);
  background: var(--mac-surface-muted);
}

.sync-btn,
.upload-btn {
  min-height: 32px;
  padding: 6px 12px;
  border: 1px solid var(--mac-border);
  border-radius: var(--mac-radius-control);
  color: var(--mac-text);
  background: var(--mac-surface);
  font-size: var(--mac-text-base);
  font-weight: 590;
  cursor: pointer;
  transition: background var(--mac-dur-fast) var(--mac-ease),
    border-color var(--mac-dur-fast) var(--mac-ease),
    color var(--mac-dur-fast) var(--mac-ease);
}

.sync-btn:hover:not(:disabled),
.upload-btn:hover:not(:disabled) {
  border-color: var(--mac-border-strong);
  background: var(--mac-surface-muted);
}

.sync-btn:disabled,
.upload-btn:disabled {
  border-color: var(--mac-border-soft);
  color: var(--mac-text-quaternary);
  background: var(--mac-surface-muted);
  cursor: default;
}

.model-label,
.knowledge-base-control label,
.streaming-label {
  color: var(--mac-text-secondary);
  font-size: var(--mac-text-base);
  white-space: nowrap;
}

.model-select,
.knowledge-base-select {
  min-height: 32px;
  padding: 5px 28px 5px 10px;
  border: 1px solid var(--mac-border);
  border-radius: var(--mac-radius-control);
  color: var(--mac-text);
  background: var(--mac-surface);
  font-size: var(--mac-text-base);
  cursor: pointer;
  transition: border-color var(--mac-dur-fast) var(--mac-ease),
    box-shadow var(--mac-dur-fast) var(--mac-ease);
}

.model-select:hover:not(:disabled),
.knowledge-base-select:hover:not(:disabled) {
  border-color: var(--mac-border-strong);
}

.model-select:focus,
.knowledge-base-select:focus {
  border-color: var(--mac-black);
  outline: none;
  box-shadow: var(--mac-focus-ring);
}

.model-select:disabled,
.knowledge-base-select:disabled {
  color: var(--mac-text-tertiary);
  background: var(--mac-surface-muted);
  cursor: default;
}

.knowledge-base-control {
  display: inline-flex;
  align-items: center;
  gap: var(--mac-space-2);
  min-width: 0;
  white-space: nowrap;
}

.knowledge-base-select {
  min-width: 150px;
  max-width: 240px;
}

.knowledge-loading {
  color: var(--mac-text-tertiary);
  font-size: var(--mac-text-sm);
}

.streaming-label {
  display: inline-flex;
  align-items: center;
  gap: var(--mac-space-2);
}

.streaming-label input {
  accent-color: var(--mac-black);
}

/* ---- Transcript --------------------------------------------------------- */

.chat-messages {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: var(--mac-space-4);
  min-height: 0;
  padding: 24px clamp(16px, 5vw, 64px);
  overflow-y: auto;
  background: var(--mac-bg);
}

.chat-history-state,
.chat-history-error {
  align-self: center;
  max-width: 440px;
  margin: auto;
  padding: 12px 14px;
  border: 1px solid var(--mac-border-soft);
  border-radius: var(--mac-radius-md);
  color: var(--mac-text-secondary);
  background: var(--mac-surface);
  box-shadow: var(--mac-elev-1);
  font-size: var(--mac-text-base);
}

.chat-history-error {
  display: flex;
  align-items: center;
  gap: var(--mac-space-3);
  border-style: dashed;
  border-color: var(--mac-border-strong);
  box-shadow: none;
}

.history-load-more {
  display: flex;
  flex-direction: column;
  align-self: stretch;
  align-items: center;
  gap: var(--mac-space-2);
}

.history-more-error {
  max-width: 440px;
  color: var(--mac-text-tertiary);
  font-size: var(--mac-text-base);
  text-align: center;
}

.message {
  max-width: min(74%, 760px);
  padding: 13px 17px;
  border-radius: var(--mac-radius-lg);
  font-size: var(--mac-text-md);
  line-height: 1.6;
  word-wrap: break-word;
}

/* User speaks in the accent (solid black); the assistant answers on paper. */
.user-message {
  align-self: flex-end;
  color: var(--mac-text-inverse);
  background: var(--mac-black);
}

.ai-message {
  align-self: flex-start;
  border: 1px solid var(--mac-border-soft);
  color: var(--mac-text);
  background: var(--mac-surface);
  box-shadow: var(--mac-elev-1);
}

.message-header {
  display: flex;
  align-items: center;
  gap: var(--mac-space-2);
  margin-bottom: 6px;
  color: var(--mac-text-tertiary);
  font-size: var(--mac-text-sm);
}

.message-header b {
  font-weight: 650;
}

.user-message .message-header {
  color: var(--mac-text-inverse);
  opacity: 0.66;
}

.tts-btn {
  padding: 2px 7px;
  border: 1px solid var(--mac-border);
  border-radius: var(--mac-radius-xs);
  color: var(--mac-text-secondary);
  background: var(--mac-surface);
  font-size: var(--mac-text-xs);
  cursor: pointer;
  transition: background var(--mac-dur-fast) var(--mac-ease),
    border-color var(--mac-dur-fast) var(--mac-ease),
    color var(--mac-dur-fast) var(--mac-ease);
}

.tts-btn:hover {
  border-color: var(--mac-border-strong);
  color: var(--mac-text);
  background: var(--mac-surface-muted);
}

/*
 * Generation is signalled by breathing opacity rather than movement — the
 * transcript is being read while this runs, so nothing should jump.
 */
.streaming-indicator {
  color: var(--mac-text-tertiary);
  font-weight: 650;
  animation: mac-pulse 1.6s var(--mac-ease) infinite;
}

@keyframes mac-pulse {
  0%,
  100% {
    opacity: 0.35;
  }

  50% {
    opacity: 1;
  }
}

.message-content {
  white-space: pre-wrap;
  word-break: break-word;
}

.message-content :deep(a) {
  color: inherit;
  text-decoration: underline;
  text-underline-offset: 2px;
}

.message-content :deep(code) {
  padding: 1px 5px;
  border-radius: var(--mac-radius-xs);
  background: var(--mac-surface-inset);
  font-family: var(--mac-font-mono);
  font-size: 0.9em;
}

.message-content :deep(pre) {
  margin: var(--mac-space-2) 0;
  padding: 12px 14px;
  overflow-x: auto;
  border-radius: var(--mac-radius-md);
  background: var(--mac-surface-inset);
  font-family: var(--mac-font-mono);
  font-size: var(--mac-text-base);
  line-height: 1.55;
}

/* Inside the black bubble the inset surface disappears; step up instead. */
.user-message .message-content :deep(code),
.user-message .message-content :deep(pre) {
  color: var(--mac-text-inverse);
  background: var(--mac-black-hover);
}

/* ---- Citations ---------------------------------------------------------- */

.citation-section {
  display: grid;
  gap: var(--mac-space-2);
  margin-top: var(--mac-space-4);
  padding-top: var(--mac-space-3);
  border-top: 1px solid var(--mac-border-soft);
}

.citation-title {
  color: var(--mac-text-tertiary);
  font-size: var(--mac-text-sm);
  font-weight: 650;
  letter-spacing: 0.04em;
}

.citation-card {
  padding: 10px 12px;
  border: 1px solid var(--mac-border-soft);
  border-radius: var(--mac-radius-md);
  background: var(--mac-surface-muted);
}

.citation-card-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--mac-space-3);
  color: var(--mac-text);
  font-size: var(--mac-text-base);
}

.citation-number {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 18px;
  height: 18px;
  margin-right: 7px;
  border-radius: var(--mac-radius-full);
  color: var(--mac-text-inverse);
  background: var(--mac-black);
  font-size: var(--mac-text-xs);
  font-variant-numeric: tabular-nums;
}

.citation-heading {
  margin-top: 6px;
  color: var(--mac-text-secondary);
  font-size: var(--mac-text-sm);
  font-weight: 590;
}

.citation-content {
  display: -webkit-box;
  margin: 6px 0 0;
  overflow: hidden;
  color: var(--mac-text-secondary);
  font-size: var(--mac-text-sm);
  line-height: 1.55;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 3;
}

.citation-score,
.citation-location {
  color: var(--mac-text-tertiary);
  font-size: var(--mac-text-xs);
  white-space: nowrap;
}

.citation-location {
  margin-top: 7px;
}

/* ---- Composer ----------------------------------------------------------- */
/*
 * The send/mic controls are overlaid on the field, so the field's right
 * padding and the buttons' offsets are both derived from one inset value.
 */

.chat-input {
  --composer-inset: clamp(16px, 5vw, 64px);
  position: relative;
  z-index: 1;
  padding: 16px var(--composer-inset);
  border-top: 1px solid var(--mac-border-soft);
  background: var(--mac-surface);
}

.chat-input textarea {
  display: block;
  width: 100%;
  min-height: 52px;
  max-height: 160px;
  padding: 14px 142px 14px 16px;
  border: 1px solid var(--mac-border);
  border-radius: var(--mac-radius-lg);
  color: var(--mac-text);
  background: var(--mac-surface-muted);
  font-size: var(--mac-text-md);
  line-height: 1.5;
  resize: none;
  outline: none;
  transition: border-color var(--mac-dur-fast) var(--mac-ease),
    background var(--mac-dur-fast) var(--mac-ease),
    box-shadow var(--mac-dur-fast) var(--mac-ease);
}

.chat-input textarea::placeholder {
  color: var(--mac-text-quaternary);
}

.chat-input textarea:focus {
  border-color: var(--mac-black);
  background: var(--mac-surface);
  box-shadow: var(--mac-focus-ring);
}

.chat-input textarea:disabled {
  color: var(--mac-text-tertiary);
}

.mic-btn,
.send-btn {
  position: absolute;
  bottom: 25px;
  height: 34px;
  border-radius: var(--mac-radius-control);
  font-size: var(--mac-text-base);
  font-weight: 590;
  cursor: pointer;
  transition: background var(--mac-dur-fast) var(--mac-ease),
    border-color var(--mac-dur-fast) var(--mac-ease),
    color var(--mac-dur-fast) var(--mac-ease);
}

.mic-btn {
  right: calc(var(--composer-inset) + 88px);
  width: 44px;
  border: 1px solid var(--mac-border);
  color: var(--mac-text-secondary);
  background: var(--mac-surface);
}

.mic-btn:hover:not(:disabled) {
  border-color: var(--mac-border-strong);
  color: var(--mac-text);
  background: var(--mac-surface-muted);
}

/* Recording is "on": solid fill plus the same quiet opacity pulse. */
.mic-btn.recording {
  border-color: var(--mac-black);
  color: var(--mac-text-inverse);
  background: var(--mac-black);
  animation: mac-pulse 1.6s var(--mac-ease) infinite;
}

.mic-btn.processing {
  border-style: dashed;
  border-color: var(--mac-border-strong);
  color: var(--mac-text);
  background: var(--mac-surface-muted);
  animation: mac-pulse 1.6s var(--mac-ease) infinite;
}

.mic-btn:disabled,
.send-btn:disabled {
  cursor: default;
}

.mic-btn:disabled {
  color: var(--mac-text-quaternary);
  background: var(--mac-surface-muted);
}

.send-btn {
  right: calc(var(--composer-inset) + 8px);
  min-width: 74px;
  padding: 0 16px;
  border: 1px solid transparent;
  color: var(--mac-text-inverse);
  background: var(--mac-black);
}

.send-btn:hover:not(:disabled) {
  background: var(--mac-black-hover);
}

.send-btn:active:not(:disabled) {
  background: var(--mac-black-active);
}

.send-btn:disabled {
  border-color: var(--mac-border-soft);
  color: var(--mac-text-quaternary);
  background: var(--mac-surface-strong);
}

/* ---- Responsive --------------------------------------------------------- */
/*
 * Under 768px the sidebar stops being a sidebar and becomes a horizontal
 * session strip pinned above the transcript.
 */

@media (max-width: 768px) {
  .ai-chat-container {
    height: 100vh;
    height: 100dvh;
    min-height: 0;
    flex-direction: column;
  }

  .session-list {
    flex: 0 0 auto;
    width: 100%;
    height: auto;
    max-height: 126px;
    border-right: 0;
    border-bottom: 1px solid var(--mac-border-soft);
  }

  .session-list-header {
    flex-direction: row;
    align-items: center;
    justify-content: space-between;
    min-height: 52px;
    padding: 8px 12px;
  }

  .new-chat-btn {
    width: auto;
  }

  .session-list-ul {
    display: flex;
    min-height: 44px;
    padding: 0 8px 8px;
    overflow-x: auto;
    overflow-y: hidden;
  }

  .session-item {
    flex: 0 0 auto;
    max-width: 190px;
  }

  .session-item + .session-item {
    margin: 0 0 0 4px;
  }

  .session-list-state,
  .session-list-error,
  .load-more-sessions-btn {
    margin: 0 8px 8px;
  }

  .top-bar {
    flex-wrap: wrap;
    max-height: 154px;
    padding: 8px 10px;
    overflow-y: auto;
  }

  .top-bar button {
    white-space: nowrap;
  }

  .knowledge-base-select {
    min-width: 130px;
    max-width: 190px;
  }

  .chat-messages {
    gap: var(--mac-space-3);
    padding: 16px;
  }

  .message {
    max-width: 88%;
    padding: 12px 14px;
  }

  .chat-input {
    --composer-inset: 12px;
    padding: 12px var(--composer-inset);
  }

  .chat-input textarea {
    padding-right: 126px;
  }

  .mic-btn,
  .send-btn {
    bottom: 21px;
  }

  .mic-btn {
    right: calc(var(--composer-inset) + 78px);
    width: 38px;
  }

  .send-btn {
    min-width: 64px;
    padding: 0 12px;
  }
}

@media (max-width: 375px) {
  .top-bar {
    max-height: 166px;
  }

  .back-btn,
  .sync-btn,
  .upload-btn,
  .model-label,
  .model-select,
  .knowledge-base-control,
  .knowledge-base-select,
  .streaming-label {
    font-size: var(--mac-text-sm);
  }

  .sync-btn,
  .upload-btn {
    padding: 6px 9px;
  }

  .message {
    max-width: 92%;
  }
}
</style>
