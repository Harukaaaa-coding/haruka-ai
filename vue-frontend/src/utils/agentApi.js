import api from './api'

const responseError = (error, fallback) => {
  const data = error?.response?.data
  const message = data?.status_msg || data?.error_code || error?.message || fallback
  const normalized = new Error(message)
  normalized.code = data?.error_code || ''
  normalized.status = error?.response?.status
  normalized.data = data
  return normalized
}

const request = async (promise, fallback) => {
  try {
    const response = await promise
    const payload = response?.data
    if (!payload || Number(payload.status_code) !== 1000) {
      const error = new Error(payload?.status_msg || payload?.error_code || fallback)
      error.code = payload?.error_code || ''
      error.data = payload
      throw error
    }
    return payload
  } catch (error) {
    // Let callers distinguish intentional cancellation from an API failure.
    // Polling views use this when a page is hidden or unmounted.
    if (error?.code === 'ERR_CANCELED' || error?.name === 'CanceledError') throw error
    if (error?.data && !error?.response) throw error
    throw responseError(error, fallback)
  }
}

const encoded = value => encodeURIComponent(String(value))

export const listAgentTasks = ({ status = '', limit = 50, offset = 0 } = {}, { signal } = {}) => {
  const params = { limit, offset }
  if (status) params.status = status
  return request(api.get('/agent/tasks', { params, signal }), '获取 Agent 任务失败')
}

export const getAgentTask = (taskId, { signal } = {}) =>
  request(api.get(`/agent/tasks/${encoded(taskId)}`, { signal }), '获取任务详情失败')

export const createAgentTask = ({ goal, model_id: modelId }) =>
  request(api.post('/agent/tasks', { goal, model_id: modelId }), '创建 Agent 任务失败')

export const approveAgentStep = (taskId, stepId) =>
  request(
    api.post(`/agent/tasks/${encoded(taskId)}/steps/${encoded(stepId)}/approve`),
    '批准步骤失败'
  )

export const rejectAgentStep = (taskId, stepId, reason) =>
  request(
    api.post(`/agent/tasks/${encoded(taskId)}/steps/${encoded(stepId)}/reject`, { reason }),
    '拒绝步骤失败'
  )

export const resumeAgentTask = (taskId, retryUnknown = false) =>
  request(
    api.post(`/agent/tasks/${encoded(taskId)}/resume`, { retry_unknown: Boolean(retryUnknown) }),
    '恢复任务失败'
  )

export const cancelAgentTask = taskId =>
  request(api.post(`/agent/tasks/${encoded(taskId)}/cancel`), '取消任务失败')

export const listAgentModels = () =>
  request(api.get('/AI/models'), '获取模型列表失败')
