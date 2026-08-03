<template>
  <main class="image-recognition-container" :aria-busy="uploading">
    <!-- 左侧会话列表 -->
    <aside class="session-list" aria-label="图像识别会话">
      <div class="session-list-header">
        <span>图像识别</span>
      </div>
      <ul class="session-list-ul">
        <li class="session-item active" aria-current="true">
          图像识别助手
        </li>
      </ul>
    </aside>

    <!-- 右侧聊天区域 -->
    <div class="chat-section">
      <header class="top-bar">
        <button type="button" class="back-btn" @click="$router.push('/menu')">← 返回</button>
        <h2>AI 图像识别助手</h2>
      </header>

      <div class="chat-messages" ref="chatContainerRef" role="log" aria-live="polite" aria-relevant="additions text">
        <div
          v-for="(message, index) in messages"
          :key="index"
          :class="['message', message.role === 'user' ? 'user-message' : 'ai-message']"
        >
          <div class="message-header">
            <b>{{ message.role === 'user' ? '你' : 'AI' }}:</b>
          </div>
          <div class="message-content">
            <span>{{ message.content }}</span>
            <img v-if="message.imageUrl" :src="message.imageUrl" alt="上传的图片" />
          </div>
        </div>
      </div>

      <div class="chat-input">
        <form @submit.prevent="handleSubmit">
          <label for="recognition-image" class="visually-hidden">选择需要识别的图片</label>
          <input
            id="recognition-image"
            ref="fileInputRef"
            type="file"
            accept="image/*"
            required
            :disabled="uploading"
            @change="handleFileSelect"
          />
          <button type="submit" :disabled="!selectedFile || uploading">{{ uploading ? '识别中...' : '发送图片' }}</button>
        </form>
      </div>
    </div>
  </main>
</template>

<script>
import { ref, nextTick, onBeforeUnmount } from 'vue'
import { ElMessage } from '../utils/elementFeedback'
import api from '../utils/api'

export default {
  name: 'ImageRecognition',
  setup() {
    const messages = ref([])
    const selectedFile = ref(null)
    const uploading = ref(false)
    const fileInputRef = ref()
    const chatContainerRef = ref()
    const objectUrls = new Set()

    const handleFileSelect = (event) => {
      const file = event.target.files[0]
      if (!file) {
        selectedFile.value = null
        return
      }
      if (!file.type.startsWith('image/')) {
        ElMessage.error('请选择有效的图片文件')
        selectedFile.value = null
        event.target.value = ''
        return
      }
      if (file.size > 10 * 1024 * 1024) {
        ElMessage.error('图片大小不能超过 10MB')
        selectedFile.value = null
        event.target.value = ''
        return
      }
      selectedFile.value = file
    }

    const handleSubmit = async () => {
      if (!selectedFile.value || uploading.value) return

      const file = selectedFile.value
      const imageUrl = URL.createObjectURL(file)
      objectUrls.add(imageUrl)
      uploading.value = true

      // Add user message to UI
      messages.value.push({
        role: 'user',
        content: `已上传图片: ${file.name}`,
        imageUrl: imageUrl,
      })

      await nextTick()
      scrollToBottom()

      // Create FormData
      const formData = new FormData()
      formData.append('image', file)

      try {
        const response = await api.post('/image/recognize', formData)


        if (response.data && response.data.class_name) {
             const aiText = `识别结果: ${response.data.class_name}`
            messages.value.push({
                role: 'assistant',
                content: aiText,
            })
        } else {
             messages.value.push({
                 role: 'assistant',
                 content: `[错误] ${response.data.status_msg || '识别失败'}`,
             })
        }
      } catch (error) {
        console.error('Upload error:', error)
        messages.value.push({
          role: 'assistant',
          content: `[错误] 无法连接到服务器或上传失败: ${error.message}`,
        })
      } finally {
        uploading.value = false
        await nextTick()
        scrollToBottom()


        selectedFile.value = null
        if (fileInputRef.value) {
          fileInputRef.value.value = ''
        }
      }
    }

    onBeforeUnmount(() => {
      objectUrls.forEach(url => URL.revokeObjectURL(url))
      objectUrls.clear()
    })

    const scrollToBottom = () => {
      if (chatContainerRef.value) {
        chatContainerRef.value.scrollTop = chatContainerRef.value.scrollHeight
      }
    }

    return {
      messages,
      selectedFile,
      uploading,
      fileInputRef,
      chatContainerRef,
      handleFileSelect,
      handleSubmit
    }
  }
}
</script>

<style scoped>
/*
 * Image recognition — a two-pane document window: a sunken sidebar of sessions
 * on the left, transcript plus composer on the right. Both chrome edges (top
 * bar and composer) carry the frosted material so the transcript reads as the
 * only surface that scrolls.
 */

.image-recognition-container {
  display: flex;
  height: 100vh;
  height: 100dvh;
  overflow: hidden;
  color: var(--mac-text);
  background: var(--mac-bg);
}

/* ---- Session sidebar ---------------------------------------------------- */

.session-list {
  display: flex;
  flex: 0 0 260px;
  flex-direction: column;
  width: 260px;
  height: 100%;
  overflow: hidden;
  border-right: 1px solid var(--mac-border-soft);
  background: var(--mac-bg-sunken);
}

.session-list-header {
  padding: 20px 18px 14px;
  border-bottom: 1px solid var(--mac-border-soft);
  font-size: var(--mac-text-lg);
  font-weight: 650;
  letter-spacing: var(--mac-tracking-tight);
}

.session-list-ul {
  flex: 1;
  min-height: 0;
  padding: var(--mac-space-2);
  overflow-y: auto;
  list-style: none;
}

.session-item {
  padding: 10px 12px;
  border: 1px solid transparent;
  border-radius: var(--mac-radius-md);
  color: var(--mac-text-secondary);
  font-size: var(--mac-text-md);
  cursor: pointer;
  transition: color var(--mac-dur-fast) var(--mac-ease),
    background-color var(--mac-dur-fast) var(--mac-ease);
}

.session-item:hover {
  color: var(--mac-text);
  background: var(--mac-surface-strong);
}

/* The selected row is a raised surface rather than a tinted one, which is how
 * macOS separates selection from hover in a sunken sidebar. */
.session-item.active {
  border-color: var(--mac-border-soft);
  color: var(--mac-text);
  background: var(--mac-surface);
  box-shadow: var(--mac-elev-1);
  font-weight: 590;
}

/* ---- Transcript column -------------------------------------------------- */

.chat-section {
  display: flex;
  flex: 1;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
  overflow: hidden;
}

.top-bar {
  display: flex;
  align-items: center;
  gap: var(--mac-space-3);
  min-height: 60px;
  padding: 10px 22px;
  border-bottom: 1px solid var(--mac-border-soft);
  background: var(--mac-material-strong);
  -webkit-backdrop-filter: var(--mac-material-blur);
  backdrop-filter: var(--mac-material-blur);
}

.top-bar h2 {
  font-size: var(--mac-text-xl);
  font-weight: 650;
  letter-spacing: var(--mac-tracking-tight);
}

.back-btn {
  padding: 8px 12px;
  border: 0;
  border-radius: var(--mac-radius-control);
  color: var(--mac-text-secondary);
  background: transparent;
  cursor: pointer;
  transition: color var(--mac-dur-fast) var(--mac-ease),
    background-color var(--mac-dur-fast) var(--mac-ease);
}

.back-btn:hover {
  color: var(--mac-text);
  background: var(--mac-surface-strong);
}

.chat-messages {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: var(--mac-space-3);
  min-height: 0;
  padding: 28px clamp(18px, 5vw, 64px);
  overflow-y: auto;
}

.message {
  max-width: min(72%, 760px);
  padding: 12px 16px;
  border-radius: var(--mac-radius-lg);
  font-size: var(--mac-text-md);
  line-height: 1.55;
  word-wrap: break-word;
}

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
  margin-bottom: var(--mac-space-1);
  font-size: var(--mac-text-sm);
}

.message-header b {
  font-weight: 590;
}

.ai-message .message-header {
  color: var(--mac-text-tertiary);
}

/* No inverse-tertiary token exists, so the speaker label on the dark bubble is
 * dimmed with opacity instead of a second hardcoded colour. */
.user-message .message-header {
  color: var(--mac-text-inverse);
  opacity: 0.64;
}

.message-content {
  white-space: pre-wrap;
  word-break: break-word;
}

.message-content img {
  display: block;
  max-width: 250px;
  margin-top: 10px;
  border: 1px solid var(--mac-border-soft);
  border-radius: var(--mac-radius-md);
  box-shadow: var(--mac-elev-1);
}

/* ---- Composer ----------------------------------------------------------- */

.chat-input {
  padding: 16px clamp(18px, 5vw, 64px);
  border-top: 1px solid var(--mac-border-soft);
  background: var(--mac-material-strong);
  -webkit-backdrop-filter: var(--mac-material-blur);
  backdrop-filter: var(--mac-material-blur);
}

.chat-input form {
  display: flex;
  gap: var(--mac-space-3);
  max-width: 980px;
  margin: 0 auto;
}

/* Dashed edge marks the file field as an empty drop target; it fills in as a
 * solid control once the browser puts a filename in it. */
.chat-input input[type='file'] {
  flex: 1;
  min-width: 0;
  padding: 9px 12px;
  border: 1px dashed var(--mac-border);
  border-radius: var(--mac-radius-control);
  color: var(--mac-text-secondary);
  background: var(--mac-surface);
  font-size: var(--mac-text-base);
  cursor: pointer;
  transition: border-color var(--mac-dur-fast) var(--mac-ease),
    background-color var(--mac-dur-fast) var(--mac-ease);
}

.chat-input input[type='file']:hover:not(:disabled) {
  border-color: var(--mac-border-strong);
  background: var(--mac-surface-muted);
}

.chat-input input[type='file']:disabled {
  color: var(--mac-text-quaternary);
  background: var(--mac-surface-muted);
  cursor: default;
}

.chat-input input[type='file']::file-selector-button {
  margin-right: var(--mac-space-3);
  padding: 6px 12px;
  border: 0;
  border-radius: var(--mac-radius-xs);
  color: var(--mac-text-inverse);
  background: var(--mac-black);
  font-size: var(--mac-text-base);
  font-weight: 590;
  cursor: pointer;
  transition: background-color var(--mac-dur-fast) var(--mac-ease);
}

.chat-input input[type='file']::file-selector-button:hover {
  background: var(--mac-black-hover);
}

.chat-input button {
  min-height: 36px;
  padding: 0 20px;
  border: 1px solid transparent;
  border-radius: var(--mac-radius-control);
  color: var(--mac-text-inverse);
  background: var(--mac-black);
  font-size: var(--mac-text-md);
  font-weight: 590;
  white-space: nowrap;
  transition: background-color var(--mac-dur-fast) var(--mac-ease),
    border-color var(--mac-dur-fast) var(--mac-ease);
}

.chat-input button:hover:not(:disabled) {
  background: var(--mac-black-hover);
}

.chat-input button:active:not(:disabled) {
  background: var(--mac-black-active);
}

.chat-input button:disabled {
  border-color: var(--mac-border-soft);
  color: var(--mac-text-quaternary);
  background: var(--mac-surface-strong);
}

/* ---- Responsive --------------------------------------------------------- */

@media (max-width: 768px) {
  .image-recognition-container {
    flex-direction: column;
    min-height: 0;
  }

  .session-list {
    flex: 0 0 auto;
    width: 100%;
    height: auto;
    max-height: 102px;
    border-right: 0;
    border-bottom: 1px solid var(--mac-border-soft);
  }

  .session-list-header {
    padding: 10px 14px;
    font-size: var(--mac-text-md);
  }

  .session-list-ul {
    display: flex;
    gap: var(--mac-space-1);
    min-height: 42px;
    padding: 6px;
    overflow-x: auto;
    overflow-y: hidden;
  }

  .session-item {
    flex: 0 0 auto;
    padding: 8px 12px;
    white-space: nowrap;
  }

  .top-bar {
    min-height: 52px;
    padding: 8px 12px;
  }

  .top-bar h2 {
    font-size: var(--mac-text-lg);
  }

  .chat-messages {
    gap: var(--mac-space-2);
    padding: 16px;
  }

  .message {
    max-width: 88%;
    padding: 11px 14px;
  }

  .message-content img {
    max-width: min(250px, 100%);
  }

  .chat-input {
    padding: 12px;
  }

  .chat-input form {
    gap: var(--mac-space-2);
  }

  .chat-input input[type='file'] {
    min-width: 0;
    padding: 8px 10px;
  }

  .chat-input button {
    padding: 0 16px;
  }
}

@media (max-width: 480px) {
  .top-bar h2 {
    font-size: var(--mac-text-md);
  }

  .back-btn {
    padding: 7px 10px;
  }

  .message {
    max-width: 92%;
  }

  .chat-input form {
    flex-direction: column;
  }

  .chat-input button {
    width: 100%;
  }
}
</style>
