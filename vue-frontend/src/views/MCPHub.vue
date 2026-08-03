<template>
  <main class="mcp-page">
    <header class="page-header">
      <button type="button" class="back-btn" @click="$router.push('/menu')">← 返回</button>
      <div><h1>MCP Hub</h1><p>动态工具发现、风险审批与脱敏审计</p></div>
      <button type="button" class="primary-btn" :disabled="loading" @click="reloadServers">重新加载 Registry</button>
    </header>

    <section class="content-grid">
      <section class="card servers-card">
        <div class="section-title"><div><h2>服务</h2><p>仅从受信配置文件加载，不在页面传递凭据</p></div></div>
        <article v-for="server in servers" :key="server.id" class="server-row">
          <span :class="['state-dot', server.state === 'ready' ? 'ready' : 'offline']"></span>
          <div class="server-main"><strong>{{ server.id }}</strong><small>{{ protocolLabel(server.protocol) }} · {{ server.tool_count || 0 }} tools · {{ server.timeout_ms }} ms</small></div>
          <span :class="['permission-pill', server.allowed ? 'allowed' : 'blocked']">{{ server.allowed ? '允许' : '禁用' }}</span>
          <button type="button" class="text-btn" :disabled="refreshingServer === server.id" @click="refreshServer(server)">{{ refreshingServer === server.id ? '刷新中…' : '刷新' }}</button>
        </article>
        <div v-if="!servers.length" class="empty">没有已配置的 MCP 服务</div>
      </section>

      <section class="card tools-card">
        <div class="section-title"><div><h2>动态工具</h2><p>工具定义来自 MCP tools/list</p></div><button type="button" class="text-btn" @click="loadTools(true)">刷新工具</button></div>
        <button v-for="tool in tools" :key="tool.name" type="button" :class="['tool-row', { active: selectedTool?.name === tool.name }]" @click="selectTool(tool)">
          <div><strong>{{ tool.name }}</strong><small>{{ tool.description || '无描述' }}</small></div>
          <span :class="['risk-pill', `risk-${tool.risk || 'low'}`]">{{ riskLabel(tool.risk) }}</span>
        </button>
        <div v-if="!tools.length" class="empty">尚未发现工具</div>
      </section>

      <section class="card call-card">
        <div class="section-title"><div><h2>工具调用</h2><p>{{ selectedTool ? selectedTool.name : '先从工具列表选择一项' }}</p></div></div>
        <template v-if="selectedTool">
          <div class="schema-box"><strong>Input Schema</strong><pre>{{ pretty(selectedTool.input_schema) }}</pre></div>
          <label for="toolArguments">参数 JSON</label>
          <textarea id="toolArguments" v-model="argumentsText" spellcheck="false" rows="7"></textarea>
          <button type="button" class="primary-btn full" :disabled="calling" @click="callSelectedTool">
            {{ calling ? '调用中…' : (selectedTool.requires_approval ? '审批后调用' : '调用工具') }}
          </button>
          <div v-if="callResult" class="result-box"><strong>调用结果</strong><pre>{{ callResult }}</pre></div>
        </template>
      </section>

      <section class="card audit-card">
        <div class="section-title"><div><h2>我的审计记录</h2><p>参数与结果默认脱敏</p></div><button type="button" class="text-btn" @click="loadAudits">刷新</button></div>
        <div class="audit-table">
          <article v-for="audit in audits" :key="audit.id || audit.request_id" class="audit-row">
            <strong>{{ audit.tool_name }}</strong>
            <span>{{ audit.outcome || audit.status }}</span>
            <span>{{ audit.duration_ms || 0 }} ms</span>
            <small>{{ formatDate(audit.created_at) }}</small>
          </article>
        </div>
        <div v-if="!audits.length" class="empty">暂无工具调用记录</div>
      </section>
    </section>
  </main>
</template>

<script>
import { onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from '../utils/elementFeedback'
import api from '../utils/api'

export default {
  name: 'MCPHubView',
  setup() {
    const servers = ref([])
    const tools = ref([])
    const audits = ref([])
    const selectedTool = ref(null)
    const argumentsText = ref('{}')
    const callResult = ref('')
    const loading = ref(false)
    const calling = ref(false)
    const refreshingServer = ref('')

    const assertSuccess = (response, fallback) => {
      if (!response?.data || response.data.status_code !== 1000) throw new Error(response?.data?.status_msg || response?.data?.error_code || fallback)
      return response.data
    }

    const loadServers = async () => {
      const payload = assertSuccess(await api.get('/mcp-hub/servers'), '获取 MCP 服务失败')
      servers.value = Array.isArray(payload.servers) ? payload.servers : []
    }
    const loadTools = async (refresh = false) => {
      const payload = assertSuccess(await api.get('/mcp-hub/tools', { params: { refresh } }), '获取 MCP 工具失败')
      tools.value = Array.isArray(payload.tools) ? payload.tools : []
      if (selectedTool.value) selectedTool.value = tools.value.find(item => item.name === selectedTool.value.name) || null
    }
    const loadAudits = async () => {
      const payload = assertSuccess(await api.get('/mcp-hub/audits', { params: { limit: 30, offset: 0 } }), '获取审计记录失败')
      audits.value = Array.isArray(payload.audits) ? payload.audits : []
    }
    const loadAll = async () => {
      loading.value = true
      try { await Promise.all([loadServers(), loadTools(false), loadAudits()]) }
      catch (error) { ElMessage.error(error.message || 'MCP Hub 加载失败') }
      finally { loading.value = false }
    }
    const reloadServers = async () => {
      loading.value = true
      try {
        const payload = assertSuccess(await api.post('/mcp-hub/servers/reload'), '重新加载 Registry 失败')
        servers.value = payload.servers || []
        await loadTools(true)
        ElMessage.success('MCP Registry 已重新加载')
      } catch (error) { ElMessage.error(error.message || '重新加载失败') }
      finally { loading.value = false }
    }
    const refreshServer = async (server) => {
      refreshingServer.value = server.id
      try {
        const payload = assertSuccess(await api.post(`/mcp-hub/servers/${encodeURIComponent(server.id)}/refresh`), '刷新服务失败')
        const index = servers.value.findIndex(item => item.id === server.id)
        if (index >= 0 && payload.server) servers.value[index] = payload.server
        await loadTools(false)
      } catch (error) { ElMessage.error(error.message || '刷新服务失败') }
      finally { refreshingServer.value = '' }
    }
    const selectTool = tool => {
      selectedTool.value = tool
      argumentsText.value = '{}'
      callResult.value = ''
    }
    const parseArguments = () => {
      const parsed = JSON.parse(argumentsText.value || '{}')
      if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object') throw new Error('参数必须是 JSON 对象')
      return parsed
    }
    const acquireApproval = async (tool, args) => {
      await ElMessageBox.confirm(`工具 ${tool.name} 风险等级为“${riskLabel(tool.risk)}”，确认本次调用吗？`, 'MCP 工具审批', {
        type: tool.destructive ? 'warning' : 'info', confirmButtonText: '批准一次', cancelButtonText: '取消'
      })
      const challenge = assertSuccess(await api.post('/mcp-hub/approvals', { tool_name: tool.name, arguments: args }), '创建审批失败').approval
      if (!challenge?.id) throw new Error('审批服务未返回 challenge ID')
      const approved = assertSuccess(await api.post(`/mcp-hub/approvals/${encodeURIComponent(challenge.id)}/approve`), '审批失败').approval
      if (!approved?.token) throw new Error('审批服务未返回一次性令牌')
      return approved.token
    }
    const callSelectedTool = async () => {
      if (!selectedTool.value) return
      calling.value = true
      callResult.value = ''
      try {
        const args = parseArguments()
        const approvalToken = selectedTool.value.requires_approval ? await acquireApproval(selectedTool.value, args) : undefined
        const payload = assertSuccess(await api.post('/mcp-hub/tools/call', {
          tool_name: selectedTool.value.name, arguments: args, ...(approvalToken ? { approval_token: approvalToken } : {})
        }), '工具调用失败')
        callResult.value = pretty(payload.result)
        ElMessage.success('工具调用完成')
        await loadAudits()
      } catch (error) {
        if (error !== 'cancel' && error !== 'close') ElMessage.error(error.message || '工具调用失败')
      } finally { calling.value = false }
    }
    const pretty = value => {
      if (typeof value === 'string') return value
      try { return JSON.stringify(value ?? {}, null, 2) } catch { return String(value) }
    }
    const protocolLabel = protocol => {
      if (!protocol) return '未连接'
      if (typeof protocol === 'string') return protocol
      return [protocol.server_name || 'MCP', protocol.protocol_version].filter(Boolean).join(' · ')
    }
    const riskLabel = risk => ({ low: '低风险', medium: '中风险', high: '高风险', critical: '关键风险', destructive: '破坏性' }[risk] || risk || '低风险')
    const formatDate = value => value ? new Date(value).toLocaleString() : '—'

    onMounted(loadAll)
    return { servers, tools, audits, selectedTool, argumentsText, callResult, loading, calling, refreshingServer, reloadServers, loadTools, loadAudits, refreshServer, selectTool, callSelectedTool, pretty, protocolLabel, riskLabel, formatDate }
  }
}
</script>

<style scoped>
/*
 * MCP Hub — a frosted window header over a two-column board of cards: server
 * registry and tool list on the left, the call console and audit log on the
 * right. Permission and risk both stay greyscale and escalate by fill weight:
 * muted fill (neutral) → outline (elevated) → solid black (highest).
 */

.mcp-page {
  min-height: 100vh;
  color: var(--mac-text);
  background: var(--mac-bg);
}

button {
  font: inherit;
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
.section-title p {
  margin-top: 4px;
  color: var(--mac-text-secondary);
  font-size: var(--mac-text-base);
}

.section-title h2 {
  font-size: var(--mac-text-lg);
}

/* ---- Controls ----------------------------------------------------------- */

.back-btn,
.text-btn {
  border: 0;
  color: var(--mac-text-secondary);
  background: transparent;
  cursor: pointer;
  transition: color var(--mac-dur-fast) var(--mac-ease),
    background-color var(--mac-dur-fast) var(--mac-ease);
}

.back-btn {
  padding: 8px 12px;
  border-radius: var(--mac-radius-control);
}

.text-btn {
  padding: 4px 8px;
  border-radius: var(--mac-radius-xs);
  font-size: var(--mac-text-base);
  font-weight: 590;
  white-space: nowrap;
}

.back-btn:hover:not(:disabled),
.text-btn:hover:not(:disabled) {
  color: var(--mac-text);
  background: var(--mac-surface-strong);
}

.primary-btn {
  padding: 9px 16px;
  border: 1px solid transparent;
  border-radius: var(--mac-radius-control);
  color: var(--mac-text-inverse);
  background: var(--mac-black);
  font-size: var(--mac-text-md);
  font-weight: 590;
  cursor: pointer;
  transition: background-color var(--mac-dur-fast) var(--mac-ease);
}

.primary-btn:hover:not(:disabled) {
  background: var(--mac-black-hover);
}

.primary-btn:active:not(:disabled) {
  background: var(--mac-black-active);
}

.primary-btn:disabled {
  border-color: var(--mac-border-soft);
  color: var(--mac-text-quaternary);
  background: var(--mac-surface-strong);
}

.text-btn:disabled {
  opacity: 0.45;
}

/* ---- Board -------------------------------------------------------------- */

.content-grid {
  display: grid;
  grid-template-columns: minmax(300px, 0.8fr) minmax(360px, 1.2fr);
  gap: var(--mac-space-4);
  max-width: 1440px;
  margin: 0 auto;
  padding: 24px;
}

.card {
  padding: 20px;
  border: 1px solid var(--mac-border-soft);
  border-radius: var(--mac-radius-xl);
  background: var(--mac-surface);
  box-shadow: var(--mac-elev-1);
}

.section-title {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--mac-space-3);
  margin-bottom: var(--mac-space-3);
}

/* ---- Server registry ---------------------------------------------------- */

.server-row {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto auto;
  align-items: center;
  gap: var(--mac-space-3);
  padding: 12px 0;
  border-top: 1px solid var(--mac-border-soft);
}

.server-main,
.tool-row > div {
  display: flex;
  flex-direction: column;
  gap: 3px;
  min-width: 0;
}

.server-main strong,
.tool-row strong {
  font-weight: 590;
}

.server-main small,
.tool-row small {
  overflow: hidden;
  color: var(--mac-text-tertiary);
  font-size: var(--mac-text-sm);
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* Reachability by fill: a hollow ring when the server is not connected, a
 * solid dot with a halo when it is. */
.state-dot {
  width: 9px;
  height: 9px;
  border-radius: 50%;
  background: transparent;
  box-shadow: inset 0 0 0 1px var(--mac-border-strong);
}

.state-dot.ready {
  background: var(--mac-black);
  box-shadow: 0 0 0 3px var(--mac-surface-strong);
}

.permission-pill,
.risk-pill {
  padding: 4px 9px;
  border: 1px solid transparent;
  border-radius: var(--mac-radius-full);
  color: var(--mac-text-secondary);
  background: var(--mac-surface-strong);
  font-size: var(--mac-text-sm);
  white-space: nowrap;
}

.permission-pill.allowed {
  color: var(--mac-text-inverse);
  background: var(--mac-black);
}

.permission-pill.blocked {
  border-style: dashed;
  border-color: var(--mac-border-strong);
  color: var(--mac-text-secondary);
  background: transparent;
}

/* ---- Tool list ---------------------------------------------------------- */

.tool-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  align-items: center;
  gap: var(--mac-space-2);
  width: 100%;
  margin-top: var(--mac-space-2);
  padding: 12px;
  border: 1px solid var(--mac-border-soft);
  border-radius: var(--mac-radius-md);
  color: var(--mac-text);
  text-align: left;
  background: var(--mac-surface);
  cursor: pointer;
  transition: border-color var(--mac-dur-fast) var(--mac-ease),
    background-color var(--mac-dur-fast) var(--mac-ease),
    box-shadow var(--mac-dur-fast) var(--mac-ease);
}

.tool-row:hover {
  border-color: var(--mac-border);
  background: var(--mac-surface-muted);
  box-shadow: var(--mac-elev-1);
}

.tool-row.active {
  border-color: var(--mac-border-strong);
  background: var(--mac-surface-strong);
}

/* Risk escalates by weight, not hue. Low keeps the neutral pill fill; medium
 * steps up to an outline; anything from high upwards goes solid. */
.risk-medium {
  border-color: var(--mac-border-strong);
  color: var(--mac-text);
  background: transparent;
}

.risk-high,
.risk-critical,
.risk-destructive {
  color: var(--mac-text-inverse);
  background: var(--mac-black);
}

/* ---- Call console ------------------------------------------------------- */

.call-card label {
  display: block;
  margin: var(--mac-space-4) 0 var(--mac-space-2);
  color: var(--mac-text-secondary);
  font-size: var(--mac-text-base);
  font-weight: 590;
}

.call-card textarea {
  width: 100%;
  padding: 10px 12px;
  border: 1px solid var(--mac-border);
  border-radius: var(--mac-radius-control);
  color: var(--mac-text);
  background: var(--mac-surface);
  font-family: var(--mac-font-mono);
  font-size: var(--mac-text-base);
  line-height: 1.5;
  resize: vertical;
  transition: border-color var(--mac-dur-fast) var(--mac-ease),
    box-shadow var(--mac-dur-fast) var(--mac-ease);
}

.call-card textarea:hover {
  border-color: var(--mac-border-strong);
}

.call-card textarea:focus {
  border-color: var(--mac-black);
  outline: none;
  box-shadow: var(--mac-focus-ring);
}

.full {
  width: 100%;
  margin-top: var(--mac-space-3);
}

.schema-box,
.result-box {
  padding: 12px;
  border: 1px solid var(--mac-border-soft);
  border-radius: var(--mac-radius-md);
  background: var(--mac-surface-inset);
  overflow: auto;
}

.schema-box strong,
.result-box strong {
  color: var(--mac-text-secondary);
  font-size: var(--mac-text-base);
  font-weight: 590;
}

.schema-box pre,
.result-box pre {
  margin-top: var(--mac-space-2);
  color: var(--mac-text);
  font-family: var(--mac-font-mono);
  font-size: var(--mac-text-sm);
  line-height: 1.55;
  white-space: pre-wrap;
  word-break: break-word;
}

/* The result is the payload the user asked for, so it sits on a plain surface
 * with a firmer edge than the schema reference above it. */
.result-box {
  margin-top: var(--mac-space-4);
  border-color: var(--mac-border);
  background: var(--mac-surface-muted);
}

/* ---- Audit log ---------------------------------------------------------- */

.audit-card {
  grid-column: 1 / -1;
}

.audit-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 130px 90px 180px;
  gap: var(--mac-space-3);
  align-items: baseline;
  padding: 10px 0;
  border-top: 1px solid var(--mac-border-soft);
}

.audit-row strong {
  overflow: hidden;
  font-weight: 590;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.audit-row span,
.audit-row small {
  color: var(--mac-text-secondary);
  font-size: var(--mac-text-base);
}

.audit-row span:last-of-type,
.audit-row small {
  color: var(--mac-text-tertiary);
  font-variant-numeric: tabular-nums;
}

.empty {
  padding: 28px;
  color: var(--mac-text-tertiary);
  font-size: var(--mac-text-base);
  text-align: center;
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

  .content-grid {
    grid-template-columns: 1fr;
    gap: var(--mac-space-3);
    padding: 14px;
  }

  .card {
    padding: 16px;
  }

  .audit-card {
    grid-column: auto;
  }

  .audit-row {
    grid-template-columns: minmax(0, 1fr) auto;
  }

  .audit-row small {
    display: none;
  }
}
</style>
