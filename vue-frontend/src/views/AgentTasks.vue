<template>
  <main class="agent-page">
    <p class="visually-hidden" role="status" aria-live="polite" aria-atomic="true">{{ taskAnnouncement }}</p>
    <header class="page-header">
      <button type="button" class="back-btn" @click="$router.push('/menu')">← 返回</button>
      <div class="page-heading">
        <h1>Agent 任务</h1>
        <p>规划任务、审核工具操作，并从持久化检查点继续执行</p>
      </div>
      <div class="header-actions">
        <button type="button" class="secondary-btn" :disabled="refreshing" @click="refreshAll">
          {{ refreshing ? '刷新中…' : '刷新' }}
        </button>
        <button type="button" class="primary-btn" @click="openCreateDialog">新建任务</button>
      </div>
    </header>

    <section class="workspace">
      <aside class="task-list-card card" aria-label="Agent 任务列表">
        <div class="section-heading task-list-heading">
          <div>
            <h2>任务列表</h2>
            <p>共 {{ total }} 项</p>
          </div>
          <label class="status-filter">
            <span class="visually-hidden">按状态筛选</span>
            <select v-model="statusFilter" @change="handleFilterChange">
              <option value="">全部状态</option>
              <option v-for="status in taskStatusOptions" :key="status" :value="status">
                {{ taskStatusLabel(status) }}
              </option>
            </select>
          </label>
        </div>

        <div v-if="listLoading && !tasks.length" class="list-state" aria-live="polite">正在加载任务…</div>
        <div v-else-if="!tasks.length" class="list-state">
          <strong>暂无任务</strong>
          <span>{{ statusFilter ? '当前筛选条件下没有任务' : '创建一个任务开始运行 Agent' }}</span>
        </div>
        <div v-else class="task-list">
          <button
            v-for="task in tasks"
            :key="task.id"
            type="button"
            :class="['task-row', { active: selectedTaskId === task.id }]"
            :aria-current="selectedTaskId === task.id ? 'true' : undefined"
            @click="selectTask(task.id)"
          >
            <span class="task-row-top">
              <strong>{{ taskTitle(task) }}</strong>
              <span :class="['status-pill', `status-${task.status}`]">{{ taskStatusLabel(task.status) }}</span>
            </span>
            <span class="task-goal">{{ task.goal }}</span>
            <span class="task-meta">
              <span>{{ task.model_id || '默认模型' }}</span>
              <time :datetime="task.updated_at || task.created_at">{{ relativeDate(task.updated_at || task.created_at) }}</time>
            </span>
          </button>
        </div>
      </aside>

      <section class="detail-column">
        <div v-if="detailLoading && !selectedTask" class="card detail-state">正在加载任务详情…</div>
        <div v-else-if="detailError" class="card detail-state error-state" role="alert">
          <strong>任务详情暂时不可用</strong>
          <span>{{ detailError }}</span>
          <button type="button" class="secondary-btn" @click="loadSelectedTask">重试</button>
        </div>
        <div v-else-if="!selectedTask" class="card detail-state">
          <strong>选择一个任务</strong>
          <span>任务的计划、执行步骤和审批请求会显示在这里。</span>
        </div>

        <template v-else>
          <article class="card task-summary">
            <div class="summary-header">
              <div>
                <span class="eyebrow">任务目标</span>
                <h2>{{ selectedTask.goal }}</h2>
              </div>
              <span :class="['status-pill', 'large', `status-${selectedTask.status}`]">
                {{ taskStatusLabel(selectedTask.status) }}
              </span>
            </div>

            <dl class="task-facts">
              <div><dt>模型</dt><dd>{{ modelName(selectedTask.model_id) }}</dd></div>
              <div><dt>步骤</dt><dd>{{ selectedSteps.length }}</dd></div>
              <div><dt>尝试次数</dt><dd>{{ selectedTask.attempts || 0 }}</dd></div>
              <div><dt>更新时间</dt><dd>{{ formatDate(selectedTask.updated_at) }}</dd></div>
            </dl>

            <div class="task-id">任务 ID：<code>{{ selectedTask.id }}</code></div>

            <div class="task-actions">
              <button
                v-if="canResume"
                type="button"
                class="primary-btn"
                :disabled="actionLoading"
                @click="resumeTask"
              >
                恢复任务
              </button>
              <button
                v-if="canCancel"
                type="button"
                class="secondary-btn"
                :disabled="actionLoading"
                @click="cancelTask"
              >
                取消任务
              </button>
              <button type="button" class="text-btn" :disabled="detailLoading" @click="loadSelectedTask">
                刷新详情
              </button>
            </div>

            <div v-if="selectedTask.final_answer" class="result-panel">
              <strong>最终结果</strong>
              <p>{{ selectedTask.final_answer }}</p>
            </div>
            <div v-if="selectedTask.error" class="result-panel error-panel">
              <strong>执行错误</strong>
              <p>{{ selectedTask.error }}</p>
            </div>
          </article>

          <article v-if="pendingApprovalSteps.length" class="card approval-card">
            <div class="section-heading">
              <div>
                <span class="eyebrow">需要你的决定</span>
                <h2>待审批操作</h2>
              </div>
              <span class="count-pill">{{ pendingApprovalSteps.length }}</span>
            </div>

            <section v-for="step in pendingApprovalSteps" :key="step.id" class="approval-item">
              <div class="approval-title">
                <div>
                  <strong>{{ step.title || step.tool_name || '工具操作' }}</strong>
                  <p>{{ step.instruction || 'Agent 请求执行以下工具操作。' }}</p>
                </div>
                <span :class="['risk-pill', `risk-${step.risk || 'low'}`]">{{ riskLabel(step.risk) }}</span>
              </div>
              <dl class="approval-facts">
                <div><dt>工具</dt><dd>{{ step.tool_name || '—' }}</dd></div>
                <div><dt>只读</dt><dd>{{ step.read_only ? '是' : '否' }}</dd></div>
                <div><dt>可重试</dt><dd>{{ step.idempotent ? '是' : '否' }}</dd></div>
              </dl>
              <div class="arguments-preview">
                <strong>参数预览（已脱敏）</strong>
                <pre>{{ previewArguments(step.arguments_preview) }}</pre>
              </div>
              <div class="approval-actions">
                <button type="button" class="primary-btn" :disabled="actionLoading" @click="approveStep(step)">
                  批准并继续
                </button>
                <button type="button" class="secondary-btn" :disabled="actionLoading" @click="rejectStep(step)">
                  拒绝
                </button>
              </div>
            </section>
          </article>

          <article class="card timeline-card">
            <div class="section-heading">
              <div>
                <span class="eyebrow">执行记录</span>
                <h2>步骤时间线</h2>
              </div>
              <span class="step-progress">{{ completedStepCount }} / {{ selectedSteps.length }} 完成</span>
            </div>

            <ol v-if="selectedSteps.length" class="timeline">
              <li v-for="step in selectedSteps" :key="step.id" :class="['timeline-item', `step-${step.status}`]">
                <span class="timeline-marker" aria-hidden="true"></span>
                <div class="timeline-content">
                  <div class="step-heading">
                    <div>
                      <span class="step-kind">{{ stepKindLabel(step.kind) }} · 第 {{ step.sequence + 1 }} 步</span>
                      <h3>{{ step.title || stepTitle(step) }}</h3>
                    </div>
                    <span :class="['status-pill', `status-${step.status}`]">{{ stepStatusLabel(step.status) }}</span>
                  </div>
                  <p v-if="step.instruction" class="step-description">{{ step.instruction }}</p>
                  <dl v-if="step.kind === 'tool'" class="step-facts">
                    <div><dt>工具</dt><dd>{{ step.tool_name || '—' }}</dd></div>
                    <div><dt>风险</dt><dd>{{ riskLabel(step.risk) }}</dd></div>
                    <div><dt>尝试</dt><dd>{{ step.attempts || 0 }}</dd></div>
                  </dl>
                  <div v-if="step.status === 'execution_unknown'" class="unknown-notice">
                    上次工具执行结果未知。恢复时重试可能产生重复副作用，请先核对外部系统状态。
                  </div>
                  <div v-if="step.result_summary" class="step-result">
                    <strong>结果</strong>
                    <p>{{ step.result_summary }}</p>
                  </div>
                  <div v-if="step.error" class="step-result step-error">
                    <strong>错误</strong>
                    <p>{{ step.error }}</p>
                  </div>
                  <div class="step-time">
                    <time v-if="step.started_at" :datetime="step.started_at">开始：{{ formatDate(step.started_at) }}</time>
                    <time v-if="step.finished_at" :datetime="step.finished_at">结束：{{ formatDate(step.finished_at) }}</time>
                  </div>
                </div>
              </li>
            </ol>
            <div v-else class="timeline-empty">规划完成后，步骤会显示在这里。</div>
          </article>
        </template>
      </section>
    </section>

    <el-dialog
      v-model="createDialogVisible"
      title="新建 Agent 任务"
      width="min(560px, calc(100vw - 32px))"
      :close-on-click-modal="false"
      @closed="resetCreateForm"
    >
      <form class="create-form" @submit.prevent="createTask">
        <label for="agentGoal">任务目标</label>
        <textarea
          id="agentGoal"
          ref="goalInput"
          v-model="createForm.goal"
          rows="6"
          maxlength="8000"
          placeholder="清楚描述希望 Agent 完成的目标、约束和期望结果"
        ></textarea>
        <span class="field-help">Agent 会先生成计划；中高风险工具操作会暂停等待审批。</span>

        <label for="agentModel">规划模型</label>
        <select id="agentModel" v-model="createForm.model_id" :disabled="modelsLoading || !modelOptions.length">
          <option v-for="model in modelOptions" :key="model.id" :value="model.id">
            {{ model.displayName }} · {{ model.id }}
          </option>
        </select>
        <span v-if="modelsLoading" class="field-help">正在加载可用模型…</span>
        <span v-else-if="!modelOptions.length" class="field-help">当前没有可用的聊天模型，请先完成模型配置。</span>

        <div class="dialog-actions">
          <button type="button" class="secondary-btn" :disabled="creating" @click="createDialogVisible = false">取消</button>
          <button type="submit" class="primary-btn" :disabled="creating || !canCreate">
            {{ creating ? '创建中…' : '创建并运行' }}
          </button>
        </div>
      </form>
    </el-dialog>
  </main>
</template>

<script>
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElDialog } from '../utils/elementDialog'
import { ElMessage, ElMessageBox } from '../utils/elementFeedback'
import {
  approveAgentStep,
  cancelAgentTask,
  createAgentTask,
  getAgentTask,
  listAgentModels,
  listAgentTasks,
  rejectAgentStep,
  resumeAgentTask
} from '../utils/agentApi'

const TASK_STATUSES = [
  'pending',
  'planning',
  'running',
  'waiting_approval',
  'requires_review',
  'succeeded',
  'failed',
  'rejected',
  'cancelled'
]

const ACTIVE_TASK_STATUSES = new Set(['pending', 'planning', 'running', 'waiting_approval', 'requires_review'])
const CANCELLABLE_TASK_STATUSES = new Set(['pending', 'planning', 'running', 'waiting_approval', 'requires_review'])
const RESUMABLE_TASK_STATUSES = new Set(['requires_review', 'failed'])
const COMPLETED_STEP_STATUSES = new Set(['succeeded', 'rejected'])

export default {
  name: 'AgentTasksView',
  components: { ElDialog },
  setup() {
    const route = useRoute()
    const router = useRouter()
    const tasks = ref([])
    const total = ref(0)
    const selectedTask = ref(null)
    const statusFilter = ref('')
    const listLoading = ref(false)
    const detailLoading = ref(false)
    const refreshing = ref(false)
    const actionLoading = ref(false)
    const detailError = ref('')
    const createDialogVisible = ref(false)
    const creating = ref(false)
    const modelOptions = ref([])
    const modelsLoading = ref(false)
    const goalInput = ref(null)
    const createForm = reactive({ goal: '', model_id: '' })
    const taskAnnouncement = ref('')
    let listPollTimer = null
    let detailPollTimer = null
    let listRequestSerial = 0
    let detailRequestSerial = 0
    let detailRequestTaskId = ''
    let listRequestPromise = null
    let detailRequestPromise = null
    let listAbortController = null
    let detailAbortController = null
    let listRequestedFilter = ''
    let pollingActive = false
    let disposed = false
    let listPollFailures = 0
    let detailPollFailures = 0
    let announcedTaskState = ''

    const LIST_POLL_INTERVAL = 6000
    const DETAIL_POLL_INTERVAL = 2000
    const MAX_POLL_INTERVAL = 30000

    const isAbortError = error => error?.code === 'ERR_CANCELED' || error?.name === 'CanceledError' || error?.name === 'AbortError'
    const isPageVisible = () => typeof document === 'undefined' || document.visibilityState !== 'hidden'
    const clearListPollTimer = () => {
      if (listPollTimer !== null) window.clearTimeout(listPollTimer)
      listPollTimer = null
    }
    const clearDetailPollTimer = () => {
      if (detailPollTimer !== null) window.clearTimeout(detailPollTimer)
      detailPollTimer = null
    }
    const backoffDelay = (base, failures) => {
      const cappedFailures = Math.min(failures, 4)
      const delay = Math.min(MAX_POLL_INTERVAL, base * (2 ** cappedFailures))
      return delay + Math.round(Math.random() * Math.min(1000, delay * 0.15))
    }

    const selectedTaskId = computed(() => String(route.params.taskId || ''))
    const selectedSteps = computed(() => {
      const steps = Array.isArray(selectedTask.value?.steps) ? selectedTask.value.steps : []
      return [...steps].sort((left, right) => Number(left.sequence || 0) - Number(right.sequence || 0))
    })
    const pendingApprovalSteps = computed(() => selectedSteps.value.filter(step =>
      step.status === 'waiting_approval' && step.requires_approval !== false
    ))
    const hasUnknownExecution = computed(() => selectedSteps.value.some(step => step.status === 'execution_unknown'))
    const completedStepCount = computed(() => selectedSteps.value.filter(step => COMPLETED_STEP_STATUSES.has(step.status)).length)
    const canCancel = computed(() => CANCELLABLE_TASK_STATUSES.has(selectedTask.value?.status))
    const canResume = computed(() => RESUMABLE_TASK_STATUSES.has(selectedTask.value?.status))
    const canCreate = computed(() => Boolean(createForm.goal.trim() && createForm.model_id))

    const taskStatusLabel = status => ({
      pending: '等待执行', planning: '规划中', running: '执行中', waiting_approval: '等待审批',
      requires_review: '需要复核', succeeded: '已完成', failed: '失败', rejected: '已拒绝', cancelled: '已取消'
    }[status] || status || '未知')

    const stepStatusLabel = status => ({
      pending: '等待执行', running: '执行中', waiting_approval: '等待审批', approved: '已批准',
      rejected: '已拒绝', succeeded: '已完成', failed: '失败', execution_unknown: '执行结果未知', skipped: '已跳过'
    }[status] || status || '未知')

    const stepKindLabel = kind => ({ plan: '规划', tool: '工具', final: '总结' }[kind] || kind || '步骤')
    const riskLabel = risk => ({ low: '低风险', medium: '中风险', high: '高风险', critical: '关键风险' }[risk] || risk || '未标记')
    const stepTitle = step => ({ plan: '生成执行计划', tool: '执行工具', final: '整理最终结果' }[step.kind] || '执行步骤')
    const taskTitle = task => {
      const goal = String(task?.goal || '').trim()
      return goal.length > 28 ? `${goal.slice(0, 28)}…` : goal || '未命名任务'
    }
    const modelName = modelId => modelOptions.value.find(model => model.id === modelId)?.displayName || modelId || '默认模型'

    const formatDate = value => {
      if (!value) return '—'
      const date = new Date(value)
      return Number.isNaN(date.getTime()) ? '—' : date.toLocaleString('zh-CN', { hour12: false })
    }
    const relativeDate = value => {
      if (!value) return '—'
      const date = new Date(value)
      if (Number.isNaN(date.getTime())) return '—'
      const seconds = Math.round((Date.now() - date.getTime()) / 1000)
      if (seconds < 60) return '刚刚'
      if (seconds < 3600) return `${Math.floor(seconds / 60)} 分钟前`
      if (seconds < 86400) return `${Math.floor(seconds / 3600)} 小时前`
      return date.toLocaleDateString('zh-CN')
    }
    const previewArguments = value => {
      if (!value) return '{}'
      if (typeof value === 'string') {
        try { return JSON.stringify(JSON.parse(value), null, 2) } catch { return value }
      }
      try { return JSON.stringify(value, null, 2) } catch { return String(value) }
    }
    const isDialogDismissal = error => error === 'cancel' || error === 'close'

    const normalizeTask = task => ({ ...task, id: String(task.id), steps: Array.isArray(task.steps) ? task.steps : [] })
    const announceTaskState = task => {
      const waitingApprovals = task.steps.filter(step => step.status === 'waiting_approval' && step.requires_approval !== false).length
      const nextState = `${task.id}:${task.status}:${waitingApprovals}`
      if (nextState === announcedTaskState) return

      const approvalSuffix = waitingApprovals ? `，有 ${waitingApprovals} 项操作等待审批` : ''
      taskAnnouncement.value = announcedTaskState
        ? `任务状态已更新为${taskStatusLabel(task.status)}${approvalSuffix}`
        : `已加载任务，当前状态为${taskStatusLabel(task.status)}${approvalSuffix}`
      announcedTaskState = nextState
    }

    const loadTasks = async ({ silent = false, selectFirst = false } = {}) => {
      const filter = statusFilter.value
      if (listRequestPromise) {
        if (listRequestedFilter === filter) return listRequestPromise
        listAbortController?.abort()
        await listRequestPromise
      }

      const requestSerial = ++listRequestSerial
      const controller = new AbortController()
      listAbortController = controller
      listRequestedFilter = filter
      listLoading.value = true
      let request
      request = (async () => {
        try {
          const payload = await listAgentTasks({ status: filter, limit: 50, offset: 0 }, { signal: controller.signal })
          if (disposed || requestSerial !== listRequestSerial) return null
          tasks.value = Array.isArray(payload.tasks) ? payload.tasks.map(normalizeTask) : []
          total.value = Number(payload.total ?? tasks.value.length)
          if (selectFirst && tasks.value.length && !tasks.value.some(task => task.id === selectedTaskId.value)) {
            await router.replace({ name: 'AgentTasks', params: { taskId: tasks.value[0].id } })
          }
          return true
        } catch (error) {
          if (isAbortError(error) || disposed || requestSerial !== listRequestSerial) return null
          if (!silent) ElMessage.error(error.message || '获取 Agent 任务失败')
          return false
        } finally {
          if (requestSerial === listRequestSerial) {
            listLoading.value = false
            listAbortController = null
          }
          if (listRequestPromise === request) listRequestPromise = null
        }
      })()
      listRequestPromise = request
      return request
    }

    const loadTask = async (taskId, { silent = false } = {}) => {
      if (!taskId) return null
      const requestedId = String(taskId)
      if (detailRequestPromise) {
        if (detailRequestTaskId === requestedId) return detailRequestPromise
        detailAbortController?.abort()
        await detailRequestPromise
      }

      const requestSerial = ++detailRequestSerial
      const controller = new AbortController()
      detailAbortController = controller
      detailRequestTaskId = requestedId
      detailLoading.value = true
      if (!silent) detailError.value = ''
      let request
      request = (async () => {
        try {
          const payload = await getAgentTask(requestedId, { signal: controller.signal })
          if (disposed || selectedTaskId.value !== requestedId || requestSerial !== detailRequestSerial) return null
          const task = normalizeTask(payload.task || payload)
          selectedTask.value = task
          detailError.value = ''
          announceTaskState(task)
          const listIndex = tasks.value.findIndex(item => item.id === requestedId)
          if (listIndex >= 0) tasks.value[listIndex] = { ...tasks.value[listIndex], ...task, steps: [] }
          return true
        } catch (error) {
          if (isAbortError(error) || disposed || selectedTaskId.value !== requestedId || requestSerial !== detailRequestSerial) return null
          if (!silent) {
            selectedTask.value = null
            detailError.value = error.message || '获取任务详情失败'
          }
          return false
        } finally {
          if (requestSerial === detailRequestSerial) {
            detailLoading.value = false
            detailRequestTaskId = ''
            detailAbortController = null
          }
          if (detailRequestPromise === request) detailRequestPromise = null
        }
      })()
      detailRequestPromise = request
      return request
    }

    const loadSelectedTask = () => loadTask(selectedTaskId.value)

    const loadModels = async () => {
      modelsLoading.value = true
      try {
        const payload = await listAgentModels()
        const models = Array.isArray(payload.models) ? payload.models : []
        modelOptions.value = models
          .filter(model => model.available !== false && model.pipeline === 'chat')
          .map(model => ({ ...model, id: String(model.id), displayName: model.displayName || model.model || String(model.id) }))
          .sort((left, right) => {
            const leftArk = left.id === 'ark' || left.provider === 'ark'
            const rightArk = right.id === 'ark' || right.provider === 'ark'
            if (leftArk !== rightArk) return leftArk ? -1 : 1
            return left.displayName.localeCompare(right.displayName, 'zh-CN')
          })
        if (!modelOptions.value.some(model => model.id === createForm.model_id)) {
          createForm.model_id = modelOptions.value[0]?.id || ''
        }
      } catch (error) {
        ElMessage.error(error.message || '获取模型列表失败')
      } finally {
        modelsLoading.value = false
      }
    }

    const selectTask = taskId => router.push({ name: 'AgentTasks', params: { taskId } })

    const handleFilterChange = async () => {
      await loadTasks({ selectFirst: true })
      if (!tasks.value.length) {
        selectedTask.value = null
        await router.replace({ name: 'AgentTasks' })
      }
    }

    const refreshAll = async () => {
      if (refreshing.value) return
      refreshing.value = true
      try {
        await Promise.all([loadTasks({ silent: true }), loadTask(selectedTaskId.value, { silent: true })])
      } finally {
        refreshing.value = false
      }
    }

    const openCreateDialog = async () => {
      createDialogVisible.value = true
      await nextTick()
      goalInput.value?.focus()
    }
    const resetCreateForm = () => {
      createForm.goal = ''
      createForm.model_id = modelOptions.value[0]?.id || ''
    }
    const createTask = async () => {
      if (!canCreate.value || creating.value) return
      creating.value = true
      try {
        const payload = await createAgentTask({ goal: createForm.goal.trim(), model_id: createForm.model_id })
        const task = normalizeTask(payload.task || payload)
        createDialogVisible.value = false
        ElMessage.success('Agent 任务已创建')
        await loadTasks({ silent: true })
        await router.push({ name: 'AgentTasks', params: { taskId: task.id } })
      } catch (error) {
        ElMessage.error(error.message || '创建 Agent 任务失败')
      } finally {
        creating.value = false
      }
    }

    const approveStep = async step => {
      try {
        await ElMessageBox.confirm(
          `确认批准工具“${step.tool_name || step.title || '未命名工具'}”本次执行吗？请先核对脱敏参数预览。`,
          '批准工具操作',
          { confirmButtonText: '批准并继续', cancelButtonText: '取消', type: step.destructive ? 'warning' : 'info' }
        )
        actionLoading.value = true
        await approveAgentStep(selectedTask.value.id, step.id)
        ElMessage.success('已批准，任务将继续执行')
        await refreshAll()
      } catch (error) {
        if (!isDialogDismissal(error)) ElMessage.error(error.message || '批准步骤失败')
      } finally {
        actionLoading.value = false
      }
    }

    const rejectStep = async step => {
      try {
        const result = await ElMessageBox.prompt(
          `拒绝工具“${step.tool_name || step.title || '未命名工具'}”后，本次任务将不会执行该操作。`,
          '拒绝工具操作',
          {
            confirmButtonText: '确认拒绝',
            cancelButtonText: '取消',
            inputPlaceholder: '请填写拒绝原因',
            inputPattern: /\S+/,
            inputErrorMessage: '请填写拒绝原因'
          }
        )
        actionLoading.value = true
        await rejectAgentStep(selectedTask.value.id, step.id, result.value.trim())
        ElMessage.success('已拒绝该操作')
        await refreshAll()
      } catch (error) {
        if (!isDialogDismissal(error)) ElMessage.error(error.message || '拒绝步骤失败')
      } finally {
        actionLoading.value = false
      }
    }

    const resumeTask = async () => {
      if (!selectedTask.value || actionLoading.value) return
      let retryUnknown = false
      try {
        if (hasUnknownExecution.value) {
          await ElMessageBox.confirm(
            '存在“执行结果未知”的工具步骤。继续并重试可能在外部系统中造成重复操作，请确认你已核对外部状态。',
            '高风险恢复确认',
            { confirmButtonText: '仍要重试并恢复', cancelButtonText: '取消', type: 'warning' }
          )
          retryUnknown = true
        }
        actionLoading.value = true
        await resumeAgentTask(selectedTask.value.id, retryUnknown)
        ElMessage.success('任务已恢复')
        await refreshAll()
      } catch (error) {
        if (!isDialogDismissal(error)) ElMessage.error(error.message || '恢复任务失败')
      } finally {
        actionLoading.value = false
      }
    }

    const cancelTask = async () => {
      if (!selectedTask.value || actionLoading.value) return
      try {
        await ElMessageBox.confirm('取消后 Agent 将停止后续步骤，已完成的外部操作不会自动撤销。', '取消任务', {
          confirmButtonText: '确认取消', cancelButtonText: '继续运行', type: 'warning'
        })
        actionLoading.value = true
        await cancelAgentTask(selectedTask.value.id)
        ElMessage.success('取消请求已提交')
        await refreshAll()
      } catch (error) {
        if (!isDialogDismissal(error)) ElMessage.error(error.message || '取消任务失败')
      } finally {
        actionLoading.value = false
      }
    }

    const hasActiveSelectedTask = () => Boolean(
      selectedTaskId.value && ACTIVE_TASK_STATUSES.has(selectedTask.value?.status)
    )

    const scheduleListPoll = (delay = LIST_POLL_INTERVAL) => {
      clearListPollTimer()
      if (disposed || !pollingActive || !isPageVisible()) return
      listPollTimer = window.setTimeout(async () => {
        listPollTimer = null
        if (disposed || !pollingActive || !isPageVisible()) return
        const result = await loadTasks({ silent: true })
        if (disposed || !pollingActive || !isPageVisible()) return
        if (result === false) listPollFailures += 1
        else if (result === true) listPollFailures = 0
        scheduleListPoll(result === false ? backoffDelay(LIST_POLL_INTERVAL, listPollFailures) : LIST_POLL_INTERVAL)
      }, delay)
    }

    const scheduleDetailPoll = (delay = DETAIL_POLL_INTERVAL) => {
      clearDetailPollTimer()
      if (disposed || !pollingActive || !isPageVisible() || !hasActiveSelectedTask()) return
      detailPollTimer = window.setTimeout(async () => {
        detailPollTimer = null
        if (disposed || !pollingActive || !isPageVisible() || !hasActiveSelectedTask()) return
        const result = await loadTask(selectedTaskId.value, { silent: true })
        if (disposed || !pollingActive || !isPageVisible() || !hasActiveSelectedTask()) return
        if (result === false) detailPollFailures += 1
        else if (result === true) detailPollFailures = 0
        scheduleDetailPoll(result === false ? backoffDelay(DETAIL_POLL_INTERVAL, detailPollFailures) : DETAIL_POLL_INTERVAL)
      }, delay)
    }

    const pausePolling = () => {
      clearListPollTimer()
      clearDetailPollTimer()
      // In-flight polling requests do not have useful results while the tab
      // is hidden; aborting them also prevents a hidden tab from piling up
      // work when network latency is high.
      listAbortController?.abort()
      detailAbortController?.abort()
    }

    const handleVisibilityChange = () => {
      if (!isPageVisible()) {
        pausePolling()
        return
      }
      listPollFailures = 0
      detailPollFailures = 0
      scheduleListPoll(0)
      scheduleDetailPoll(0)
    }

    const startPolling = () => {
      if (pollingActive) return
      pollingActive = true
      document.addEventListener('visibilitychange', handleVisibilityChange)
      handleVisibilityChange()
    }

    const stopPolling = () => {
      pollingActive = false
      clearListPollTimer()
      clearDetailPollTimer()
      document.removeEventListener('visibilitychange', handleVisibilityChange)
      listAbortController?.abort()
      detailAbortController?.abort()
    }

    watch(
      () => route.params.taskId,
      taskId => {
        detailError.value = ''
        announcedTaskState = ''
        if (!taskId) {
          detailAbortController?.abort()
          selectedTask.value = null
          return
        }
        void loadTask(String(taskId)).finally(() => {
          if (pollingActive && !disposed && isPageVisible()) scheduleDetailPoll(DETAIL_POLL_INTERVAL)
        })
      },
      { immediate: true }
    )

    onMounted(async () => {
      await Promise.all([loadModels(), loadTasks({ selectFirst: !selectedTaskId.value })])
      startPolling()
    })
    onBeforeUnmount(() => {
      disposed = true
      stopPolling()
    })

    return {
      tasks, total, selectedTask, selectedTaskId, selectedSteps, pendingApprovalSteps, completedStepCount,
      statusFilter, taskStatusOptions: TASK_STATUSES, listLoading, detailLoading, refreshing, actionLoading,
      detailError, taskAnnouncement, createDialogVisible, creating, createForm, modelOptions, modelsLoading, goalInput,
      canCancel, canResume, canCreate, taskStatusLabel, stepStatusLabel, stepKindLabel, riskLabel, stepTitle,
      taskTitle, modelName, formatDate, relativeDate, previewArguments, selectTask, handleFilterChange,
      refreshAll, loadSelectedTask, openCreateDialog, resetCreateForm, createTask, approveStep, rejectStep,
      resumeTask, cancelTask
    }
  }
}
</script>

<style scoped>
/* ==========================================================================
   Agent Tasks — frosted page header over a two-column workspace:
   a sticky task list and a detail column (summary, approvals, timeline).
   Status is expressed by fill weight: solid = active, muted = neutral,
   dashed = stopped or unavailable. No hue anywhere.
   ========================================================================== */

.agent-page {
  min-height: 100vh;
  color: var(--mac-text);
  background: var(--mac-bg);
}

/* ---- Window header ------------------------------------------------------ */

.page-header {
  position: sticky;
  top: 0;
  z-index: 10;
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  align-items: center;
  gap: var(--mac-space-5);
  min-height: 72px;
  padding: 14px clamp(16px, 3vw, 30px);
  border-bottom: 1px solid var(--mac-border-soft);
  background: var(--mac-material-strong);
  -webkit-backdrop-filter: var(--mac-material-blur);
  backdrop-filter: var(--mac-material-blur);
}

.page-heading h1,
.section-heading h2,
.summary-header h2 {
  margin: 0;
  font-weight: 650;
  letter-spacing: var(--mac-tracking-tight);
}

.page-heading h1 {
  font-size: var(--mac-text-2xl);
  letter-spacing: var(--mac-tracking-tighter);
}

.page-heading p,
.section-heading p {
  margin: 4px 0 0;
  color: var(--mac-text-secondary);
  font-size: var(--mac-text-base);
}

/* ---- Controls ----------------------------------------------------------- */

.header-actions,
.task-actions,
.approval-actions,
.dialog-actions {
  display: flex;
  justify-content: flex-end;
  gap: var(--mac-space-2);
}

.back-btn,
.text-btn {
  min-height: 32px;
  padding: 7px 11px;
  border: 1px solid transparent;
  border-radius: var(--mac-radius-control);
  color: var(--mac-text-secondary);
  background: transparent;
  font-size: var(--mac-text-base);
  cursor: pointer;
  transition: background var(--mac-dur-fast) var(--mac-ease),
    color var(--mac-dur-fast) var(--mac-ease);
}

.back-btn:hover,
.text-btn:hover:not(:disabled) {
  color: var(--mac-text);
  background: var(--mac-surface-muted);
}

.primary-btn,
.secondary-btn {
  min-height: 34px;
  padding: 8px 14px;
  border-radius: var(--mac-radius-control);
  font-size: var(--mac-text-md);
  font-weight: 590;
  cursor: pointer;
  transition: background var(--mac-dur-fast) var(--mac-ease),
    border-color var(--mac-dur-fast) var(--mac-ease),
    color var(--mac-dur-fast) var(--mac-ease);
}

.primary-btn {
  border: 1px solid transparent;
  color: var(--mac-text-inverse);
  background: var(--mac-black);
}

.primary-btn:hover:not(:disabled) {
  background: var(--mac-black-hover);
}

.primary-btn:active:not(:disabled) {
  background: var(--mac-black-active);
}

.secondary-btn {
  border: 1px solid var(--mac-border);
  color: var(--mac-text);
  background: var(--mac-surface);
}

.secondary-btn:hover:not(:disabled) {
  border-color: var(--mac-border-strong);
  background: var(--mac-surface-muted);
}

button:disabled {
  opacity: 0.45;
  cursor: default;
}

.status-filter select,
.create-form select {
  min-height: 34px;
  padding: 6px 28px 6px 10px;
  border: 1px solid var(--mac-border);
  border-radius: var(--mac-radius-control);
  color: var(--mac-text);
  background: var(--mac-surface);
  font-size: var(--mac-text-base);
  cursor: pointer;
  transition: border-color var(--mac-dur-fast) var(--mac-ease),
    box-shadow var(--mac-dur-fast) var(--mac-ease);
}

.status-filter select:hover:not(:disabled),
.create-form select:hover:not(:disabled) {
  border-color: var(--mac-border-strong);
}

.status-filter select:focus,
.create-form select:focus,
.create-form textarea:focus {
  border-color: var(--mac-black);
  outline: none;
  box-shadow: var(--mac-focus-ring);
}

/* ---- Workspace ---------------------------------------------------------- */

.workspace {
  display: grid;
  grid-template-columns: 330px minmax(0, 1fr);
  gap: var(--mac-space-5);
  width: min(1480px, 100%);
  margin: 0 auto;
  padding: var(--mac-space-6) clamp(16px, 3vw, 30px);
}

.card {
  border: 1px solid var(--mac-border-soft);
  border-radius: var(--mac-radius-lg);
  background: var(--mac-surface);
  box-shadow: var(--mac-elev-1);
}

.task-list-card {
  position: sticky;
  top: 96px;
  display: flex;
  flex-direction: column;
  align-self: start;
  max-height: calc(100vh - 120px);
  overflow: hidden;
}

.section-heading {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--mac-space-3);
}

.section-heading h2 {
  margin-top: 2px;
  font-size: var(--mac-text-xl);
}

.task-list-heading {
  padding: 16px 18px;
  border-bottom: 1px solid var(--mac-border-soft);
}

.task-list {
  padding: var(--mac-space-2);
  overflow-y: auto;
}

.task-row {
  display: grid;
  gap: 6px;
  width: 100%;
  padding: 11px 12px;
  border: 1px solid transparent;
  border-radius: var(--mac-radius-md);
  color: var(--mac-text);
  background: transparent;
  text-align: left;
  cursor: pointer;
  transition: background var(--mac-dur-fast) var(--mac-ease),
    border-color var(--mac-dur-fast) var(--mac-ease);
}

.task-row + .task-row {
  margin-top: 2px;
}

.task-row:hover {
  background: var(--mac-surface-muted);
}

.task-row.active {
  border-color: var(--mac-border-soft);
  background: var(--mac-surface-strong);
}

.task-row-top,
.task-meta {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--mac-space-2);
}

.task-row-top strong,
.task-goal {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.task-row-top strong {
  font-size: var(--mac-text-md);
  font-weight: 590;
}

.task-goal,
.task-meta {
  color: var(--mac-text-tertiary);
  font-size: var(--mac-text-sm);
}

.list-state,
.detail-state,
.timeline-empty {
  display: grid;
  place-content: center;
  gap: var(--mac-space-2);
  color: var(--mac-text-tertiary);
  font-size: var(--mac-text-md);
  text-align: center;
}

.list-state {
  min-height: 220px;
  padding: var(--mac-space-6);
}

.list-state strong,
.detail-state strong {
  color: var(--mac-text);
}

/* ---- Detail column ------------------------------------------------------ */

.detail-column {
  display: grid;
  align-content: start;
  gap: var(--mac-space-5);
  min-width: 0;
}

.detail-state {
  min-height: 320px;
  padding: 30px;
}

.detail-state .secondary-btn {
  justify-self: center;
  margin-top: 6px;
}

.task-summary,
.approval-card,
.timeline-card {
  padding: 22px;
}

.summary-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--mac-space-4);
}

.summary-header h2 {
  max-width: 900px;
  margin-top: 5px;
  font-size: clamp(20px, 2.5vw, 26px);
  line-height: 1.32;
}

.eyebrow,
.step-kind {
  color: var(--mac-text-tertiary);
  font-size: var(--mac-text-sm);
  font-weight: 650;
  letter-spacing: 0.04em;
}

/* ---- Status & risk pills ------------------------------------------------ */

.status-pill,
.risk-pill,
.count-pill {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  padding: 3px 9px;
  border: 1px solid var(--mac-border);
  border-radius: var(--mac-radius-full);
  color: var(--mac-text-secondary);
  background: var(--mac-surface-strong);
  font-size: var(--mac-text-sm);
  white-space: nowrap;
}

.status-pill.large {
  padding: 5px 11px;
  font-size: var(--mac-text-base);
}

/* Live or waiting on someone: solid fill, the heaviest weight available. */
.status-running,
.status-planning,
.status-waiting_approval,
.status-requires_review,
.status-execution_unknown,
.risk-high,
.risk-critical {
  border-color: transparent;
  color: var(--mac-text-inverse);
  background: var(--mac-black);
}

/* Stopped or refused: outlined and dashed, never filled. */
.status-failed,
.status-rejected,
.status-cancelled,
.risk-medium {
  border-style: dashed;
  border-color: var(--mac-border-strong);
  color: var(--mac-text);
  background: transparent;
}

/* ---- Fact grids --------------------------------------------------------- */

.task-facts,
.approval-facts,
.step-facts {
  display: grid;
  gap: var(--mac-space-3);
  margin: 0;
}

.task-facts {
  grid-template-columns: repeat(4, minmax(0, 1fr));
  margin-top: 20px;
  padding: var(--mac-space-4) 0;
  border-top: 1px solid var(--mac-border-soft);
  border-bottom: 1px solid var(--mac-border-soft);
}

.task-facts div,
.approval-facts div,
.step-facts div {
  min-width: 0;
}

dt {
  margin-bottom: 3px;
  color: var(--mac-text-tertiary);
  font-size: var(--mac-text-sm);
}

dd {
  margin: 0;
  overflow: hidden;
  color: var(--mac-text);
  font-size: var(--mac-text-md);
  text-overflow: ellipsis;
}

.task-id {
  margin-top: var(--mac-space-3);
  color: var(--mac-text-tertiary);
  font-size: var(--mac-text-sm);
  overflow-wrap: anywhere;
}

.task-id code {
  font-family: var(--mac-font-mono);
}

.task-actions {
  margin-top: var(--mac-space-4);
}

/* ---- Result & notice panels --------------------------------------------- */

.result-panel,
.unknown-notice,
.step-result,
.arguments-preview {
  margin-top: var(--mac-space-4);
  padding: 13px 14px;
  border: 1px solid var(--mac-border-soft);
  border-radius: var(--mac-radius-md);
  background: var(--mac-surface-muted);
  font-size: var(--mac-text-md);
}

.result-panel strong,
.step-result strong,
.arguments-preview strong {
  font-size: var(--mac-text-base);
  font-weight: 650;
}

.result-panel p,
.step-result p {
  margin: 6px 0 0;
  color: var(--mac-text-secondary);
  line-height: 1.6;
  white-space: pre-wrap;
}

.error-panel,
.step-error,
.error-state,
.unknown-notice {
  border-style: dashed;
  border-color: var(--mac-border-strong);
}

.unknown-notice {
  color: var(--mac-text-secondary);
  font-size: var(--mac-text-base);
  line-height: 1.55;
}

/* ---- Approvals ---------------------------------------------------------- */

.approval-card {
  border-color: var(--mac-border);
  box-shadow: var(--mac-elev-2);
}

.count-pill {
  min-width: 26px;
  border-color: transparent;
  color: var(--mac-text-inverse);
  background: var(--mac-black);
  font-variant-numeric: tabular-nums;
}

.approval-item {
  margin-top: var(--mac-space-4);
  padding-top: var(--mac-space-4);
  border-top: 1px solid var(--mac-border-soft);
}

.approval-title {
  display: flex;
  justify-content: space-between;
  gap: var(--mac-space-4);
}

.approval-title p {
  margin: 5px 0 0;
  color: var(--mac-text-secondary);
  font-size: var(--mac-text-base);
  line-height: 1.5;
}

.approval-facts {
  grid-template-columns: repeat(3, minmax(0, 1fr));
  margin-top: var(--mac-space-3);
}

.arguments-preview {
  background: var(--mac-surface-inset);
}

.arguments-preview pre {
  max-height: 220px;
  margin: var(--mac-space-2) 0 0;
  overflow: auto;
  color: var(--mac-text-secondary);
  font-family: var(--mac-font-mono);
  font-size: var(--mac-text-sm);
  line-height: 1.55;
  white-space: pre-wrap;
  word-break: break-word;
}

.approval-actions {
  margin-top: var(--mac-space-3);
}

/* ---- Timeline ----------------------------------------------------------- */

.step-progress {
  color: var(--mac-text-secondary);
  font-size: var(--mac-text-base);
  font-variant-numeric: tabular-nums;
}

.timeline {
  margin: var(--mac-space-5) 0 0;
  padding: 0;
  list-style: none;
}

.timeline-item {
  position: relative;
  display: grid;
  grid-template-columns: 18px minmax(0, 1fr);
  gap: var(--mac-space-3);
}

.timeline-item:not(:last-child) {
  padding-bottom: 22px;
}

/* The rail is drawn behind the markers so each marker punches through it. */
.timeline-item:not(:last-child)::before {
  content: '';
  position: absolute;
  top: 15px;
  bottom: 0;
  left: 6px;
  width: 1px;
  background: var(--mac-border);
}

.timeline-marker {
  position: relative;
  z-index: 1;
  width: 13px;
  height: 13px;
  margin-top: 4px;
  border: 2px solid var(--mac-border-strong);
  border-radius: var(--mac-radius-full);
  background: var(--mac-surface);
}

.step-running .timeline-marker,
.step-waiting_approval .timeline-marker,
.step-succeeded .timeline-marker,
.step-approved .timeline-marker {
  border-color: var(--mac-black);
  background: var(--mac-black);
}

.step-failed .timeline-marker,
.step-rejected .timeline-marker,
.step-execution_unknown .timeline-marker {
  border-style: dashed;
  border-color: var(--mac-black);
}

.timeline-content {
  min-width: 0;
  padding-bottom: 3px;
}

.step-heading {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--mac-space-3);
}

.step-heading h3 {
  margin: 3px 0 0;
  font-size: var(--mac-text-lg);
}

.step-description {
  margin: var(--mac-space-2) 0 0;
  color: var(--mac-text-secondary);
  font-size: var(--mac-text-md);
  line-height: 1.6;
  white-space: pre-wrap;
}

.step-facts {
  grid-template-columns: repeat(3, minmax(0, 1fr));
  margin-top: 11px;
}

.step-time {
  display: flex;
  flex-wrap: wrap;
  gap: var(--mac-space-3);
  margin-top: 10px;
  color: var(--mac-text-tertiary);
  font-size: var(--mac-text-sm);
}

.timeline-empty {
  min-height: 150px;
}

/* ---- Create dialog form ------------------------------------------------- */
/* Dialog chrome itself is themed globally in styles/element.css. */

.create-form {
  display: grid;
  gap: var(--mac-space-2);
}

.create-form label {
  margin-top: var(--mac-space-2);
  font-size: var(--mac-text-base);
  font-weight: 590;
}

.create-form textarea {
  width: 100%;
  padding: 11px 12px;
  border: 1px solid var(--mac-border);
  border-radius: var(--mac-radius-control);
  color: var(--mac-text);
  background: var(--mac-surface);
  font-size: var(--mac-text-md);
  line-height: 1.6;
  resize: vertical;
  transition: border-color var(--mac-dur-fast) var(--mac-ease),
    box-shadow var(--mac-dur-fast) var(--mac-ease);
}

.create-form textarea::placeholder {
  color: var(--mac-text-quaternary);
}

.field-help {
  color: var(--mac-text-tertiary);
  font-size: var(--mac-text-sm);
  line-height: 1.5;
}

.dialog-actions {
  margin-top: var(--mac-space-4);
  padding-top: var(--mac-space-4);
  border-top: 1px solid var(--mac-border-soft);
}

/* ---- Responsive --------------------------------------------------------- */

@media (max-width: 900px) {
  .page-header {
    grid-template-columns: auto minmax(0, 1fr);
    padding: 12px 14px;
  }

  .header-actions {
    grid-column: 1 / -1;
  }

  .workspace {
    grid-template-columns: 1fr;
    gap: var(--mac-space-4);
    padding: 14px;
  }

  .task-list-card {
    position: static;
    max-height: 380px;
  }
}

@media (max-width: 620px) {
  .page-heading p {
    display: none;
  }

  .header-actions {
    justify-content: stretch;
  }

  .header-actions button {
    flex: 1;
  }

  .task-list-heading,
  .summary-header,
  .approval-title,
  .step-heading {
    flex-direction: column;
    align-items: flex-start;
  }

  .status-filter,
  .status-filter select {
    width: 100%;
  }

  .task-summary,
  .approval-card,
  .timeline-card {
    padding: 17px;
  }

  .task-facts {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .approval-facts,
  .step-facts {
    grid-template-columns: 1fr;
  }

  .task-actions,
  .approval-actions {
    flex-wrap: wrap;
    justify-content: stretch;
  }

  .task-actions button,
  .approval-actions button {
    flex: 1 1 130px;
  }
}
</style>
