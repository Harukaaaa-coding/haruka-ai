<template>
  <main class="catalog-page">
    <header class="page-header">
      <button type="button" class="back-btn" @click="$router.push('/menu')">← 返回</button>
      <div><h1>模型目录</h1><p>Provider、模型与能力管线已经解耦</p></div>
      <button type="button" class="primary-btn" :disabled="loading" @click="loadCatalog">刷新状态</button>
    </header>
    <section class="catalog-content">
      <div class="provider-row">
        <article v-for="provider in providers" :key="provider.id" class="provider-card">
          <span :class="['provider-state', { ready: provider.implemented }]"></span>
          <div><strong>{{ provider.displayName }}</strong><small>{{ provider.protocol }}</small></div>
          <span>{{ provider.implemented ? '已实现' : '适配骨架' }}</span>
        </article>
      </div>
      <div class="model-grid">
        <article v-for="model in models" :key="model.id" :class="['model-card', { unavailable: !model.available }]">
          <header><div><h2>{{ model.displayName }}</h2><p>{{ model.provider }} · {{ model.model || model.modelId }}</p></div><span :class="['availability', model.available ? 'ready' : 'offline']">{{ model.available ? '可用' : '未配置' }}</span></header>
          <div class="capabilities">
            <span v-for="capability in enabledCapabilities(model.capabilities)" :key="capability">{{ capabilityLabel(capability) }}</span>
          </div>
          <p v-if="!model.available" class="reason">{{ model.unavailableReason || '缺少对应的本地配置或凭据' }}</p>
          <footer><code>{{ model.id }}</code><span>{{ model.pipeline || 'chat' }}</span></footer>
        </article>
      </div>
      <div v-if="!models.length && !loading" class="empty">模型目录为空</div>
    </section>
  </main>
</template>

<script>
import { onMounted, ref } from 'vue'
import { ElMessage } from '../utils/elementFeedback'
import api from '../utils/api'

export default {
  name: 'ModelCatalogView',
  setup() {
    const providers = ref([])
    const models = ref([])
    const loading = ref(false)
    const loadCatalog = async () => {
      loading.value = true
      try {
        const response = await api.get('/AI/models')
        if (response.data?.status_code !== 1000) throw new Error(response.data?.status_msg || '模型目录加载失败')
        providers.value = response.data.providers || []
        models.value = response.data.models || []
      } catch (error) { ElMessage.error(error.message || '模型目录加载失败') }
      finally { loading.value = false }
    }
    const enabledCapabilities = capabilities => Object.entries(capabilities || {}).filter(([, enabled]) => enabled).map(([name]) => name)
    const capabilityLabel = name => ({ chat: '对话', streaming: '流式', toolCalling: '工具', retrieval: '检索', local: '本地', vision: '视觉', json: 'JSON' }[name] || name)
    onMounted(loadCatalog)
    return { providers, models, loading, loadCatalog, enabledCapabilities, capabilityLabel }
  }
}
</script>

<style scoped>
.catalog-page {
  min-height: 100vh;
  color: var(--mac-text);
  background: var(--mac-bg);
}

.page-header {
  position: sticky;
  top: 0;
  z-index: 10;
  min-height: 76px;
  padding: 14px clamp(16px, 4vw, 30px);
  display: grid;
  grid-template-columns: auto 1fr auto;
  gap: 20px;
  align-items: center;
  background: var(--mac-material-strong);
  border-bottom: 1px solid var(--mac-border-soft);
  -webkit-backdrop-filter: var(--mac-material-blur);
  backdrop-filter: var(--mac-material-blur);
}

.page-header h1 {
  margin: 0;
  font-size: var(--mac-text-xl);
  font-weight: 650;
  letter-spacing: var(--mac-tracking-tight);
}

.page-header p {
  margin: 3px 0 0;
  color: var(--mac-text-secondary);
  font-size: var(--mac-text-base);
}

.back-btn {
  padding: 7px 11px;
  border: 0;
  border-radius: var(--mac-radius-control);
  color: var(--mac-text-secondary);
  background: transparent;
  font-size: var(--mac-text-md);
  transition: color var(--mac-dur-fast) var(--mac-ease),
    background var(--mac-dur-fast) var(--mac-ease);
}

.back-btn:hover {
  color: var(--mac-text);
  background: var(--mac-surface-strong);
}

.primary-btn {
  min-height: 32px;
  padding: 0 15px;
  border: 1px solid transparent;
  border-radius: var(--mac-radius-control);
  color: var(--mac-white);
  background: var(--mac-accent-bg);
  box-shadow: var(--mac-accent-ring);
  font-size: var(--mac-text-md);
  font-weight: 590;
  letter-spacing: -0.01em;
  transition: background var(--mac-dur-fast) var(--mac-ease);
}

.primary-btn:hover:not(:disabled) {
  background: var(--mac-accent-bg-hover);
}

.primary-btn:disabled {
  color: rgba(255, 255, 255, 0.72);
  background: #b0b0b5;
  box-shadow: none;
}

.catalog-content {
  max-width: 1440px;
  margin: 0 auto;
  padding: 26px;
}

.provider-row {
  display: flex;
  gap: 12px;
  padding-bottom: 18px;
  overflow-x: auto;
}

.provider-card {
  min-width: 220px;
  display: grid;
  grid-template-columns: auto 1fr auto;
  align-items: center;
  gap: 10px;
  padding: 13px 14px;
  border: 1px solid var(--mac-border-soft);
  border-radius: var(--mac-radius-md);
  background: var(--mac-surface);
  box-shadow: var(--mac-elev-1);
}

.provider-card div {
  display: flex;
  flex-direction: column;
}

.provider-card small,
.provider-card > span:last-child {
  color: var(--mac-text-tertiary);
  font-size: 12px;
}

.provider-card > span:last-child {
  white-space: nowrap;
}

.provider-state {
  width: 9px;
  height: 9px;
  border: 1px solid var(--mac-text-tertiary);
  border-radius: 50%;
  background: transparent;
}

.provider-state.ready {
  border-color: var(--mac-black);
  background: var(--mac-black);
  box-shadow: 0 0 0 4px var(--mac-surface-strong);
}

.model-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(290px, 1fr));
  gap: 16px;
}

.model-card {
  display: flex;
  flex-direction: column;
  padding: 20px;
  border: 1px solid var(--mac-border-soft);
  border-radius: var(--mac-radius-lg);
  background: var(--mac-surface);
  box-shadow: var(--mac-elev-1);
  transition: border-color var(--mac-dur-fast) var(--mac-ease),
    box-shadow var(--mac-dur-fast) var(--mac-ease);
}

.model-card:hover {
  border-color: var(--mac-border);
  box-shadow: var(--mac-elev-2);
}

.model-card.unavailable {
  background: var(--mac-surface-muted);
  box-shadow: none;
}

.model-card.unavailable h2 {
  color: var(--mac-text-secondary);
}

.model-card header {
  display: flex;
  justify-content: space-between;
  gap: 12px;
}

.model-card h2 {
  margin: 0;
  font-size: 18px;
  letter-spacing: -0.015em;
}

.model-card header p {
  margin: 5px 0;
  color: var(--mac-text-secondary);
}

.availability {
  flex: 0 0 auto;
  height: max-content;
  padding: 4px 9px;
  border: 1px solid var(--mac-border);
  border-radius: var(--mac-radius-full);
  color: var(--mac-text-secondary);
  background: var(--mac-surface-muted);
  font-size: var(--mac-text-sm);
  font-weight: 500;
  white-space: nowrap;
}

.availability.ready {
  border-color: var(--mac-black);
  color: var(--mac-white);
  background: var(--mac-black);
}

.availability.offline {
  border-style: dashed;
  color: var(--mac-text-secondary);
  background: var(--mac-surface);
}

.capabilities {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin: 15px 0;
}

.capabilities span {
  padding: 4px 8px;
  border-radius: var(--mac-radius-xs);
  color: var(--mac-text-secondary);
  background: var(--mac-surface-strong);
  font-size: var(--mac-text-sm);
}

.reason {
  color: var(--mac-text-secondary);
  font-size: var(--mac-text-base);
}

/* Pushed to the bottom so footers align across a row of uneven cards. */
.model-card footer {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
  margin-top: auto;
  padding-top: 14px;
  border-top: 1px solid var(--mac-border-soft);
  color: var(--mac-text-tertiary);
  font-size: var(--mac-text-sm);
}

.model-card code {
  overflow: hidden;
  color: var(--mac-text-secondary);
  font-family: var(--mac-font-mono);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.empty {
  padding: clamp(40px, 8vw, 72px) 24px;
  color: var(--mac-text-tertiary);
  font-size: var(--mac-text-md);
  text-align: center;
}

button {
  font: inherit;
}

@media (max-width: 700px) {
  .page-header {
    grid-template-columns: auto 1fr;
    padding: 14px;
  }

  .page-header > .primary-btn {
    grid-column: 1 / -1;
  }

  .catalog-content {
    padding: 14px;
  }
}
</style>
