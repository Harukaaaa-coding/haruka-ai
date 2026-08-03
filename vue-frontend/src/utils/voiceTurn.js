// Browser-side realtime voice turn transport. The server owns VAD, ASR, LLM,
// TTS profile selection, and any Fish Audio control markers; this client only
// sends 16 kHz mono PCM16 plus the bounded control frames defined by the
// realtime voice protocol.

const DEFAULT_REALTIME_PATH = '/api/v1/AI/voice/realtime'
const TARGET_SAMPLE_RATE = 16000
const PCM_FRAME_SAMPLES = 320 // 20 ms at 16 kHz
const READY_TIMEOUT_MS = 10_000
const MAX_SOCKET_BUFFERED_BYTES = 512 * 1024
const CAPTURE_PROCESSOR_NAME = 'gopherai-voice-turn-capture'

const captureWorkletSource = String.raw`
class GopherAIVoiceTurnCapture extends AudioWorkletProcessor {
  constructor(options) {
    super()
    const configuredRate = options.processorOptions && options.processorOptions.targetSampleRate
    this.targetSampleRate = configuredRate || 16000
    this.ratio = sampleRate / this.targetSampleRate
    this.position = 0
    this.frameSamples = (options.processorOptions && options.processorOptions.frameSamples) || 320
    this.frame = new Int16Array(this.frameSamples)
    this.frameOffset = 0
  }

  process(inputs, outputs) {
    const input = inputs[0] && inputs[0][0]
    const output = outputs[0]
    if (output) {
      for (const channel of output) channel.fill(0)
    }
    if (!input || input.length === 0) return true

    for (let position = this.position; position < input.length; position += this.ratio) {
      const left = Math.floor(position)
      const right = Math.min(left + 1, input.length - 1)
      const fraction = position - left
      const sample = input[left] * (1 - fraction) + input[right] * fraction
      const clipped = Math.max(-1, Math.min(1, sample))
      this.frame[this.frameOffset++] = clipped < 0 ? clipped * 0x8000 : clipped * 0x7fff

      if (this.frameOffset === this.frameSamples) {
        this.port.postMessage(this.frame.buffer, [this.frame.buffer])
        this.frame = new Int16Array(this.frameSamples)
        this.frameOffset = 0
      }
    }
    this.position -= input.length
    while (this.position < 0) this.position += this.ratio
    return true
  }
}

registerProcessor('${CAPTURE_PROCESSOR_NAME}', GopherAIVoiceTurnCapture)
`

export class VoiceTurnError extends Error {
  constructor(code, message, cause) {
    super(message)
    this.name = 'VoiceTurnError'
    this.code = code
    if (cause) this.cause = cause
  }
}

export const realtimeVoiceURL = (path = DEFAULT_REALTIME_PATH) => {
  if (typeof window === 'undefined') return path
  const url = new URL(path, window.location.href)
  if (url.protocol === 'https:') url.protocol = 'wss:'
  else if (url.protocol === 'http:') url.protocol = 'ws:'
  return url.toString()
}

export const supportsRealtimeVoice = () => {
  if (typeof window === 'undefined' || !navigator.mediaDevices?.getUserMedia) return false
  const AudioContextClass = window.AudioContext || window.webkitAudioContext
  return Boolean(AudioContextClass && window.AudioWorkletNode)
}

export const createTurnID = () => {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return `turn-${crypto.randomUUID()}`
  }
  return `turn-${Date.now()}-${Math.random().toString(36).slice(2, 12)}`
}

const safeCallback = (callback, value) => {
  if (typeof callback !== 'function') return
  try {
    callback(value)
  } catch (error) {
    // A UI callback must never tear down microphone capture or the socket.
    console.error('Voice turn callback failed:', error)
  }
}

const toArrayBuffer = async value => {
  if (value instanceof ArrayBuffer) return value
  if (ArrayBuffer.isView(value)) {
    return value.buffer.slice(value.byteOffset, value.byteOffset + value.byteLength)
  }
  if (typeof Blob !== 'undefined' && value instanceof Blob) return value.arrayBuffer()
  throw new VoiceTurnError('invalid_audio', 'Realtime voice returned an unsupported audio frame.')
}

const isPCM16 = metadata => {
  const format = String(metadata?.format || '').trim().toLowerCase()
  const contentType = String(metadata?.contentType || '').trim().toLowerCase()
  return format === 'pcm16' || format === 'pcm_s16le' || contentType.includes('audio/l16')
}

// VoiceTurnClient intentionally represents one browser turn. A fresh instance
// for each click makes interruption deterministic and releases microphone
// resources even if a browser/proxy drops a WebSocket unexpectedly.
export class VoiceTurnClient {
  constructor({ url, onEvent, onError, onClose } = {}) {
    this.url = url || realtimeVoiceURL()
    this.onEvent = onEvent
    this.onError = onError
    this.onClose = onClose

    this.socket = null
    this.turnId = ''
    this.captureEnabled = false
    this.captureStream = null
    this.audioContext = null
    this.captureSource = null
    this.captureNode = null
    this.silentSink = null
    this.pendingAudioMetadata = []
    this.binaryChain = Promise.resolve()
    this.audioChain = Promise.resolve()
    this.playbackGeneration = 0
    this.playbackSources = new Set()
    this.nextPlaybackTime = 0
    this.readyResolve = null
    this.readyReject = null
    this.readyTimer = null
    this.closing = false
    this.closed = false
  }

  async start({
    turnId = createTurnID(),
    sessionId = '',
    modelId = '',
    modelType = '',
    knowledgeBaseIds,
    voiceProfileId = '',
    csrfToken = ''
  } = {}) {
    if (this.turnId) {
      throw new VoiceTurnError('turn_active', 'A realtime voice turn is already active.')
    }
    if (!supportsRealtimeVoice()) {
      throw new VoiceTurnError('unsupported', 'This browser does not support AudioWorklet realtime voice.')
    }
    if (!String(modelId || modelType).trim()) {
      throw new VoiceTurnError('invalid_start', 'A chat model is required for realtime voice.')
    }

    this.turnId = String(turnId)
    this.closed = false
    this.closing = false
    try {
      await this.prepareCapture()
      const ready = await this.connectAndStart({
        type: 'start',
        turnId: this.turnId,
        sessionId: sessionId || undefined,
        modelId: modelId || undefined,
        modelType: modelType || undefined,
        knowledgeBaseIds: Array.isArray(knowledgeBaseIds) ? knowledgeBaseIds : undefined,
        voiceProfileId: voiceProfileId || undefined,
        csrfToken: csrfToken || undefined,
        audioFormat: 'pcm16',
        sampleRate: TARGET_SAMPLE_RATE,
        channels: 1
      })
      this.captureEnabled = true
      return ready
    } catch (error) {
      await this.close({ stopPlayback: true, notifyServer: false })
      if (error instanceof VoiceTurnError) throw error
      throw new VoiceTurnError('start_failed', 'Unable to start realtime voice.', error)
    }
  }

  async commit() {
    this.requireActiveTurn()
    this.captureEnabled = false
    await this.stopCapture()
    this.sendControl({ type: 'commit', turnId: this.turnId })
  }

  async interrupt() {
    if (!this.turnId) return
    this.captureEnabled = false
    await this.stopCapture()
    this.stopPlayback()
    if (this.socket?.readyState === WebSocket.OPEN) {
      try {
        this.sendControl({ type: 'interrupt', turnId: this.turnId })
      } catch (error) {
        this.emitError(error)
      }
    }
    await this.close({ stopPlayback: true, notifyServer: true })
  }

  ping() {
    this.requireActiveTurn()
    this.sendControl({ type: 'ping', turnId: this.turnId })
  }

  async close({ stopPlayback = true, notifyServer = true } = {}) {
    if (this.closed) return
    this.closed = true
    this.captureEnabled = false
    await this.stopCapture()

    const socket = this.socket
    this.socket = null
    if (socket && socket.readyState === WebSocket.OPEN) {
      if (notifyServer && this.turnId) {
        try {
          socket.send(JSON.stringify({ type: 'close' }))
        } catch {
          // The close below is still enough to release the server-side turn.
        }
      }
      this.closing = true
      socket.close(1000, 'voice turn finished')
    }

    this.clearReadyWait()
    this.readyResolve = null
    this.readyReject = null
    this.turnId = ''

    if (stopPlayback) {
      this.pendingAudioMetadata = []
      this.stopPlayback()
      await this.closeAudioContext()
    } else {
      this.closeAudioContextAfterPlayback()
    }
  }

  requireActiveTurn() {
    if (!this.turnId || this.closed) {
      throw new VoiceTurnError('inactive_turn', 'No realtime voice turn is active.')
    }
  }

  async prepareCapture() {
    const AudioContextClass = window.AudioContext || window.webkitAudioContext
    if (!AudioContextClass || !window.AudioWorkletNode) {
      throw new VoiceTurnError('unsupported', 'AudioWorklet is not available in this browser.')
    }

    this.captureStream = await navigator.mediaDevices.getUserMedia({
      audio: {
        channelCount: 1,
        echoCancellation: true,
        noiseSuppression: true,
        autoGainControl: true
      }
    })
    this.audioContext = new AudioContextClass({ latencyHint: 'interactive' })
    if (!this.audioContext.audioWorklet) {
      throw new VoiceTurnError('unsupported', 'AudioWorklet is not available in this browser.')
    }
    await this.audioContext.resume()

    const workletURL = URL.createObjectURL(new Blob([captureWorkletSource], { type: 'application/javascript' }))
    try {
      await this.audioContext.audioWorklet.addModule(workletURL)
    } finally {
      URL.revokeObjectURL(workletURL)
    }

    this.captureSource = this.audioContext.createMediaStreamSource(this.captureStream)
    this.captureNode = new window.AudioWorkletNode(this.audioContext, CAPTURE_PROCESSOR_NAME, {
      numberOfInputs: 1,
      numberOfOutputs: 1,
      channelCount: 1,
      channelCountMode: 'explicit',
      processorOptions: {
        targetSampleRate: TARGET_SAMPLE_RATE,
        frameSamples: PCM_FRAME_SAMPLES
      }
    })
    this.silentSink = this.audioContext.createGain()
    this.silentSink.gain.value = 0
    this.captureNode.port.onmessage = event => this.sendPCMFrame(event.data)
    this.captureSource.connect(this.captureNode)
    this.captureNode.connect(this.silentSink)
    this.silentSink.connect(this.audioContext.destination)
  }

  async stopCapture() {
    if (this.captureNode) {
      this.captureNode.port.onmessage = null
      this.captureNode.disconnect()
    }
    if (this.captureSource) this.captureSource.disconnect()
    if (this.silentSink) this.silentSink.disconnect()
    if (this.captureStream) this.captureStream.getTracks().forEach(track => track.stop())
    this.captureNode = null
    this.captureSource = null
    this.silentSink = null
    this.captureStream = null
  }

  sendPCMFrame(frame) {
    if (!this.captureEnabled || !this.socket || this.socket.readyState !== WebSocket.OPEN) return
    if (!(frame instanceof ArrayBuffer) || frame.byteLength === 0) return
    if (this.socket.bufferedAmount > MAX_SOCKET_BUFFERED_BYTES) {
      this.emitEvent({ type: 'audio.backpressure', turnId: this.turnId })
      return
    }
    try {
      this.socket.send(frame)
    } catch (error) {
      this.emitError(new VoiceTurnError('audio_send_failed', 'Unable to send microphone audio.', error))
    }
  }

  connectAndStart(startFrame) {
    return new Promise((resolve, reject) => {
      let settled = false
      const settle = (callback, value) => {
        if (settled) return
        settled = true
        this.clearReadyWait()
        this.readyResolve = null
        this.readyReject = null
        callback(value)
      }
      this.readyResolve = value => settle(resolve, value)
      this.readyReject = error => settle(reject, error)
      this.readyTimer = window.setTimeout(() => {
        this.readyReject?.(new VoiceTurnError('ready_timeout', 'Realtime voice did not become ready in time.'))
      }, READY_TIMEOUT_MS)

      try {
        this.socket = new WebSocket(this.url)
        this.socket.binaryType = 'arraybuffer'
        this.socket.onopen = () => {
          try {
            this.sendControl(startFrame)
          } catch (error) {
            this.readyReject?.(error)
          }
        }
        this.socket.onmessage = event => this.handleMessage(event)
        this.socket.onerror = () => {
          const error = new VoiceTurnError('socket_error', 'Realtime voice connection failed.')
          if (this.readyReject) this.readyReject(error)
          else this.emitError(error)
        }
        this.socket.onclose = event => {
          const wasStarting = Boolean(this.readyReject)
          if (wasStarting) {
            this.readyReject(new VoiceTurnError('socket_closed', 'Realtime voice closed before it was ready.'))
          } else if (!this.closing && !this.closed) {
            this.emitError(new VoiceTurnError('socket_closed', `Realtime voice connection closed (${event.code}).`))
          }
          safeCallback(this.onClose, event)
        }
      } catch (error) {
        this.readyReject?.(new VoiceTurnError('socket_error', 'Realtime voice connection failed.', error))
      }
    })
  }

  sendControl(frame) {
    if (!this.socket || this.socket.readyState !== WebSocket.OPEN) {
      throw new VoiceTurnError('socket_unavailable', 'Realtime voice connection is unavailable.')
    }
    this.socket.send(JSON.stringify(frame))
  }

  handleMessage(event) {
    if (typeof event.data === 'string') {
      this.handleControlEvent(event.data)
      return
    }
    this.binaryChain = this.binaryChain
      .catch(() => undefined)
      .then(() => this.handleAudioFrame(event.data))
      .catch(error => this.emitError(error))
  }

  handleControlEvent(raw) {
    let payload
    try {
      payload = JSON.parse(raw)
    } catch (error) {
      this.emitError(new VoiceTurnError('invalid_event', 'Realtime voice returned invalid JSON.', error))
      return
    }
    if (!payload || typeof payload.type !== 'string') {
      this.emitError(new VoiceTurnError('invalid_event', 'Realtime voice returned an invalid event.'))
      return
    }
    if (payload.type === 'tts.audio') this.pendingAudioMetadata.push(payload)
    if (payload.type === 'ready' && payload.turnId === this.turnId) this.readyResolve?.(payload)
    if (payload.type === 'error') {
      const error = new VoiceTurnError(payload.code || 'server_error', payload.message || 'Realtime voice failed.')
      if (this.readyReject) this.readyReject(error)
      else this.emitError(error)
    }
    this.emitEvent(payload)
  }

  async handleAudioFrame(raw) {
    const metadata = this.pendingAudioMetadata.shift()
    if (!metadata) {
      throw new VoiceTurnError('unexpected_audio', 'Realtime voice returned audio without metadata.')
    }
    const audio = await toArrayBuffer(raw)
    if (Number.isFinite(Number(metadata.byteLength)) && Number(metadata.byteLength) !== audio.byteLength) {
      throw new VoiceTurnError('invalid_audio', 'Realtime voice audio length did not match metadata.')
    }
    this.enqueuePlayback(audio, metadata)
  }

  enqueuePlayback(audio, metadata) {
    const generation = this.playbackGeneration
    this.audioChain = this.audioChain
      .catch(() => undefined)
      .then(async () => {
        if (generation !== this.playbackGeneration || !this.audioContext || this.audioContext.state === 'closed') return
        const buffer = await this.decodeAudio(audio, metadata)
        if (generation !== this.playbackGeneration || !buffer) return
        this.schedulePlayback(buffer)
      })
      .catch(error => this.emitError(new VoiceTurnError('audio_decode_failed', 'Unable to play synthesized audio.', error)))
  }

  async decodeAudio(audio, metadata) {
    if (!this.audioContext) return null
    if (isPCM16(metadata)) {
      const frames = Math.floor(audio.byteLength / 2)
      if (!frames) throw new VoiceTurnError('invalid_audio', 'Realtime voice returned empty PCM audio.')
      const sampleRate = Number(metadata.sampleRate) || TARGET_SAMPLE_RATE
      const buffer = this.audioContext.createBuffer(1, frames, sampleRate)
      const target = buffer.getChannelData(0)
      const source = new DataView(audio)
      for (let index = 0; index < frames; index++) target[index] = source.getInt16(index * 2, true) / 0x8000
      return buffer
    }
    return this.audioContext.decodeAudioData(audio.slice(0))
  }

  schedulePlayback(buffer) {
    if (!this.audioContext || !buffer || this.audioContext.state === 'closed') return
    const source = this.audioContext.createBufferSource()
    source.buffer = buffer
    source.connect(this.audioContext.destination)
    const startAt = Math.max(this.audioContext.currentTime + 0.01, this.nextPlaybackTime)
    this.nextPlaybackTime = startAt + buffer.duration
    this.playbackSources.add(source)
    source.onended = () => {
      this.playbackSources.delete(source)
    }
    source.start(startAt)
  }

  stopPlayback() {
    this.playbackGeneration++
    this.nextPlaybackTime = this.audioContext?.currentTime || 0
    for (const source of this.playbackSources) {
      try {
        source.stop()
      } catch {
        // A completed AudioBufferSourceNode cannot be stopped again.
      }
    }
    this.playbackSources.clear()
  }

  closeAudioContextAfterPlayback() {
    const context = this.audioContext
    if (!context || context.state === 'closed') return
    const closeWhenPlaybackEnds = () => {
      if (this.audioContext !== context || context.state === 'closed') return
      const delay = Math.max(0, this.nextPlaybackTime - context.currentTime) * 1000 + 80
      window.setTimeout(() => {
        if (this.audioContext !== context || context.state === 'closed') return
        if (this.playbackSources.size === 0 && context.currentTime >= this.nextPlaybackTime) {
          void this.closeAudioContext()
          return
        }
        closeWhenPlaybackEnds()
      }, delay)
    }

    // `turn.completed` follows the final tts.audio metadata/binary pair on
    // the socket, but binary decoding is asynchronous. Wait for the binary
    // chain to enqueue every segment first, then wait for the resulting audio
    // chain before calculating the final playback deadline.
    void this.binaryChain
      .catch(() => undefined)
      .then(() => this.audioChain.catch(() => undefined))
      .then(closeWhenPlaybackEnds)
  }

  async closeAudioContext() {
    const context = this.audioContext
    this.audioContext = null
    if (context && context.state !== 'closed') {
      try {
        await context.close()
      } catch {
        // Browsers can reject close during page teardown; tracks are already stopped.
      }
    }
  }

  clearReadyWait() {
    if (this.readyTimer !== null) window.clearTimeout(this.readyTimer)
    this.readyTimer = null
  }

  emitEvent(event) {
    safeCallback(this.onEvent, event)
  }

  emitError(error) {
    safeCallback(this.onError, error)
  }
}
