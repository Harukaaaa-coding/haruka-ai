# 实时语音回合（WebSocket）

## 当前实现范围

后端提供一个受 JWT 保护的双向 WebSocket 端点，用于把浏览器采集的
PCM 音频组织为一次语音回合，并复用已有的会话、RAG、LLM 流式输出和消息
持久化能力：

```text
浏览器 PCM16 音频
  → WebSocket / VAD 状态提示
  → commit 后的百度批量 ASR
  → 已有会话/LLM 流式文本
  → 可选 Fish Audio S2 分句合成
  → WebSocket 文本事件与音频二进制帧
```

端点是：

```text
GET /api/v1/AI/voice/realtime
```

它位于既有的 `/api/v1/AI` JWT 保护组中。浏览器通常使用现有的 HttpOnly
Cookie 会话；原生客户端也可以使用 `Authorization: Bearer ...`。

> 重要：这里的“实时”指浏览器与服务端之间使用双向 WebSocket，并且 LLM
> 的文本分片、Fish 的分句结果会即时推送。当前默认 ASR **不是**流式识别：
> 客户端发送 `commit` 后，服务端才把累计 PCM 封装为 WAV 并调用百度短音频
> HTTP API。它不会产生 `asr.partial`，也不会因为 VAD 暂停自动提交。

当前也没有 MPS Think-First 或任何端到端语音语言模型 Provider；这类模型应在
后续作为独立的实时 Provider 接入，而不能声称已由本端点实现。

## 客户端协议

连接建立后，第一条文本帧必须是 `start`，并且必须在 10 秒内到达。控制帧为
UTF-8 JSON，大小上限 8 KiB，未知字段会被拒绝。

### `start`

```json
{
  "type": "start",
  "turnId": "turn-001",
  "sessionId": "existing-session-id",
  "modelId": "rag",
  "knowledgeBaseIds": ["knowledge-base-id"],
  "voiceProfileId": "fish-s2-warm",
  "csrfToken": "cookie-session-required",
  "audioFormat": "pcm16",
  "sampleRate": 16000,
  "channels": 1
}
```

字段规则：

- `turnId` 与 `modelId`（或兼容字段 `modelType`）必填。
- `sessionId` 可省略；省略时，ASR 得到最终文本后才创建新会话，并发送
  `session.created`。
- `knowledgeBaseIds` 最多 32 个，继续沿用文本聊天的 RAG 选择语义。
- 音频格式固定为 `pcm16`、单声道、16 kHz；省略三项时使用相同默认值。
- `voiceProfileId` 可省略，默认是 `fish-s2-natural`。
- Cookie 会话必须在本帧提供正确的 `csrfToken`。Bearer 客户端不使用该项。

`start` 被接受后，服务端返回：

```json
{
  "type": "ready",
  "turnId": "turn-001",
  "sessionId": "existing-session-id",
  "asrProvider": "baidu-batch",
  "audioFormat": "pcm16",
  "sampleRate": 16000,
  "channels": 1,
  "voiceProfileId": "fish-s2-warm",
  "ttsAvailable": true
}
```

`ttsAvailable: false` 不会阻止文本对话：它表示 Fish 未配置或当前不可用，服务端
仍会完成 ASR 和文本 LLM 回合，并额外发送 `tts.unavailable`。

`asrProvider` 是本回合由服务器配置选择的输入 Provider。客户端不能在控制帧中
任意指定供应商；当前内置 `baidu-batch`，并通过 `ASRProvider` registry 为后续流式
或其他供应商保留扩展点。

### 音频、提交与打断

`start` 后发送 WebSocket 二进制帧，内容为 PCM16LE。单帧最大 64 KiB，一次回合
累计最多 10 MiB。客户端完成讲话时显式发送：

```json
{"type":"commit","turnId":"turn-001"}
```

服务端随后发送 `turn.processing`，并开始批量 ASR、会话创建（如需要）、LLM 与
可选 TTS。

当前服务器包含基于 RMS 能量的轻量 VAD，只会发送：

```text
vad.speech_started
vad.speech_stopped
```

这些事件可用于 UI 指示，不会替代 `commit`。它不是 Silero/FSMN 等模型 VAD，噪声
环境中的模型 VAD 和真正流式 ASR 是后续 Provider 工作。

用户打断当前回合时发送：

```json
{"type":"interrupt","turnId":"turn-001"}
```

这会取消正在进行的 ASR、LLM 或 TTS context，服务端发送 `turn.cancelled`。处理中的
回合完全退出前，不能启动下一回合；客户端应等待取消事件后再发送新的 `start`。

另外支持：

```json
{"type":"ping","turnId":"turn-001"}
{"type":"close","turnId":"turn-001"}
```

前者得到应用层 `pong` 事件，后者关闭当前连接。

### 服务端事件与音频顺序

服务端事件均为 JSON 文本帧，具有 `type`，有回合关联时包含 `turnId`。常见事件顺序：

```text
ready
vad.speech_started / vad.speech_stopped
turn.processing
asr.final
session.created                 （仅新会话）
assistant.delta                 （可多次）
assistant.citations             （可选）
tts.start
tts.audio → 紧跟一个二进制音频帧
tts.end                          （每个分句）
turn.completed
```

`tts.audio` 是二进制帧的元数据，包含 `segmentId`、`contentType`、`format`、
`provider` 与 `byteLength`。**紧随其后的下一条二进制 WebSocket 消息**就是该
segment 的完整音频内容。客户端应按此顺序入播放队列，不应把未知二进制帧当作 PCM
输入或控制消息。

失败会发送 `error`，常见 `code` 包括 `invalid_control`、`start_required`、
`csrf_failed`、`invalid_audio`、`asr_failed`、`chat_failed` 与 `turn_active`。

## Fish Audio S2 表达层

当前可用的公开 profile 是固定白名单：

| Profile | 表达计划 |
| --- | --- |
| `text-only` | 不请求 TTS，只返回 ASR/文本事件 |
| `fish-s2-natural` | 自然表达（默认） |
| `fish-s2-warm` | 温暖、轻柔、较低音量 |
| `fish-s2-empathetic` | 共情、对话式、较慢语速 |
| `fish-s2-energetic` | 兴奋、活力、较快语速 |

Vue 聊天页默认发送 `fish-s2-natural`。可在前端构建/开发环境设置
`VUE_APP_VOICE_PROFILE` 选择上述任一固定 profile；它只是 profile ID，不接受
浏览器传入原始 Fish 标记或参数。

服务端将 LLM 的 `assistant.delta` 按句子缓冲后再调用 Fish。送入 Fish 的表达计划会由
`voicecontrol` 白名单渲染；其中用户、ASR 或 LLM 文本里的任意方括号内容都会先被
移除。`assistant.delta` 本身仍是文本聊天输出，前端不得把其中的方括号当作可执行的
语音控制。客户端不能提交原始 Fish 标签、供应商 URL、模型参数或凭据。

当前 Fish Adapter 使用 `/v1/tts` 的 **HTTP 非流式**请求：每个句子获得一段完整
MP3 后，通过本项目的 WebSocket 发回。因此它改善了逐句开始播放的等待时间，但不是
Fish 上游 WebSocket 音频流。接入 Fish 原生流式接口需要另一个 TTS Provider。

### 环境变量

Fish 输出只有在 API Key 与授权音色 Reference ID 都配置后才启用。建议通过部署平台
secret 或本地 `config.local.toml` 注入，不要把真实值提交到仓库：

```dotenv
GOPHERAI_FISH_AUDIO_API_KEY=your-fish-audio-api-key
GOPHERAI_FISH_AUDIO_BASE_URL=https://api.fish.audio
GOPHERAI_FISH_AUDIO_MODEL=s2-pro
GOPHERAI_FISH_AUDIO_REFERENCE_ID=your-authorized-voice-id
GOPHERAI_VOICE_ALLOWED_ORIGINS=https://app.example.com
GOPHERAI_VOICE_ASR_PROVIDER=baidu-batch
```

- `GOPHERAI_FISH_AUDIO_API_KEY`：仅服务端使用，绝不能发给浏览器。
- `GOPHERAI_FISH_AUDIO_REFERENCE_ID`：必须是已获得明确授权的音色。
- `GOPHERAI_FISH_AUDIO_BASE_URL` 默认 `https://api.fish.audio`；它是 API 根路径，
  服务端会使用 `/v1/tts`。
- `GOPHERAI_FISH_AUDIO_MODEL` 默认 `s2-pro`。
- `GOPHERAI_VOICE_ALLOWED_ORIGINS`：逗号分隔的精确浏览器 Origin 白名单。
- `GOPHERAI_VOICE_ASR_PROVIDER`：服务器选择的 ASR provider ID；默认
  `baidu-batch`。只有已在服务端 registry 注册的 ID 才会实际可用。

Fish 单个完整响应最多 32 MiB，默认 HTTP 超时为 30 秒。配置缺失时并不会回退为旧的
百度长文本 TTS；实时端点保留文本输出并报告 TTS 不可用。

## 浏览器与部署安全

- 生产环境必须使用 HTTPS/WSS。生产模式只接受 HTTPS Origin。
- Cookie 会话的 WebSocket 握手是 `GET`，普通 HTTP CSRF 中间件不会覆盖它；因此
  服务端同时验证 `Origin` 与第一条 `start` 的 `csrfToken`。
- 生产环境拒绝缺失 Origin 的浏览器连接；同源 Origin 自动允许，跨域 UI 必须显式加入
  `GOPHERAI_VOICE_ALLOWED_ORIGINS`。本地开发才允许 loopback Origin 便利行为。
- 不要把 JWT 放进 WebSocket URL query。浏览器应使用已有的 HttpOnly Cookie；原生
  客户端可在握手中使用 Bearer Authorization header。
- 浏览器需要在安全上下文中申请麦克风权限，并应把 PCM 处理放入 `AudioWorklet`；生产
  反向代理必须透传 `Upgrade`/`Connection`，并允许 WebSocket 长连接。
- Vue `AIChat.vue` 已内置实时 WebSocket 客户端：它使用 `AudioWorklet` 将麦克风下采样为
  16 kHz 单声道 PCM16，以二进制帧发送；收到 `tts.audio` 元数据及紧随其后的二进制帧后，
  会按到达顺序播放。第一次点击麦克风开始实时录制，第二次点击提交本回合；在
  ASR/LLM/TTS 处理中再次点击可中断并停止播放。
- 该客户端仅提交固定协议字段和 `voiceProfileId`，不会提交任何 Fish 方括号控制标记、供应商
  URL、模型参数或凭据。浏览器不支持 `AudioWorklet`、实时端点不可用或启动失败时，界面会
  自动保留并切换到原有“录音结束后上传 WAV”的百度批量 ASR 降级流程。
- 本地开发的 Vue 代理已为 `/api` 启用 WebSocket upgrade（`ws: true`）；生产反向代理仍需
  正确透传 `Upgrade`/`Connection`。

## 资源限制与优雅停机

实时连接不复用普通 HTTP cost guard 的 request-lifetime 槽，以免一个长连接长期占用
聊天请求并发。当前进程内 Voice Hub 限制为：

- 最多 32 个实时连接；
- 每个用户最多 1 个实时连接；
- 首个 `start` 最多等待 10 秒；
- 单帧 PCM 最大 64 KiB，单回合累计音频最大 10 MiB；
- 一次回合 ASR/LLM/TTS 的 context 最长 5 分钟；
- WebSocket 输出写入超时为 15 秒。

这些连接上限目前是**单进程**状态；多副本部署前应增加粘性路由或共享的分布式连接
租约与音频秒数限流。

收到 `SIGINT` 或 `SIGTERM` 时，服务会先将 readiness 置为不可用，再让 Voice Hub：

1. 拒绝新的 WebSocket upgrade；
2. 取消所有活动语音回合；
3. 向活动客户端发送 WebSocket close code `1001`（Going Away）并关闭 socket；
4. 在 HTTP 关停的同一宽限期内等待连接释放；
5. 仅在此后继续关闭 Redis、MySQL 等共享资源。

客户端收到 `1001` 或 `turn.cancelled` 时，应停止录音与播放，不应自动重放未完成的
语音回合。

## 下一步：真正的实时 ASR 与 MPS

接入流式 ASR 时，应新增 `ASRStream` Provider：将 PCM 帧直接转发给供应商并发出
`asr.partial`/`asr.final`，同时保留当前 WebSocket 控制帧与会话出口。接入模型 VAD 时，
可替换当前 Energy VAD，不应改变客户端协议。

MPS Think-First 或其他端到端语音语言模型应作为独立的 `end_to_end` Provider：负责其
自身的语音理解/推理/音频输出，但仍需映射为本项目的 `asr.final`、文本、音频、取消和
完成事件。它不能被误当作可任意拆换的百度 ASR、LLM 与 Fish TTS 组合，也不能默认暴露
内部思考过程或把它落库。
