# 企业级 Agent 工作流：持久化与崩溃恢复审计

> 审计日期：2026-08-04 ｜ 代码基线：`1c6ddec`
> 范围：`model/agent.go`、`dao/agent/agent.go`、`service/agent/{graph,worker,service,plan}.go`

---

## 0. 名词扫盲

用**快递仓库**类比：任务 = 包裹，Worker = 分拣员。

| 名词 | 大白话 | 不做会怎样 |
|---|---|---|
| Lease（租约） | 分拣员拿起包裹时贴一张「我的，30 秒内有效」的便利贴 | 分拣员猝死，包裹永远没人管 |
| Heartbeat（心跳） | 每 10 秒重贴一次便利贴，证明「我还活着」 | 同上 |
| Fencing Token（隔离令牌） | 每次有人拿起包裹，柜台编号 +1。旧分拣员醒来想写「我处理完了」，柜台一看编号是旧的 → 拒收 | **僵尸 Worker** 把别人做完的活覆盖成错误状态 |
| CAS（比较并交换） | `UPDATE ... WHERE 状态=我以为的状态`。返回「影响 0 行」就说明有人抢先改了 | 后写的盖掉先写的 |
| Checkpoint（检查点） | 存档点。图跑到一半，把「我在第几个节点」存下来 | 崩溃后只能从头跑 |
| 幂等（Idempotent） | 做 10 次 = 做 1 次。查余额幂等；转账不幂等 | 崩溃重试 → **转账转两次** |
| at-least-once | 分布式铁律：崩溃重试必然「至少执行一次，可能多次」。想要 exactly-once 必须靠幂等键去重 | — |
| TOCTOU | 检查时是 A，使用时变成 B | 审批时看到「删 1 条」，执行时变成「删全库」 |

**核心设计一句话**：MySQL 是唯一权威，Eino Checkpoint 只是加速缓存，随时可丢。

---

## 1. 表结构与索引

只有 **2 张表**，没有独立 checkpoint 表。

### `agent_tasks`（`model/agent.go:43`）

```go
// 业务状态
Status, CurrentStepID, PlanSummary, FinalAnswer, ErrorMessage

// 并发控制三件套 ← 最重要
Checkpoint        []byte   // Eino 序列化状态（longblob）
CheckpointVersion uint64   // 只计数，不参与 CAS
RunVersion        uint64   // ★ 真正的 fencing token

// 租约
LeaseOwner  string
LeaseUntil  *time.Time
HeartbeatAt *time.Time
```

### `agent_steps`（`model/agent.go:75`）

```go
Sequence          int     // 0 = plan 步，1..N = tool 步
Kind              string  // plan / tool / final
Status            string  // 9 种状态，含 execution_unknown
ToolArgumentsJSON string  // 完整参数（不出 JSON，防泄露）
ArgumentsDigest   string  // char(64) = SHA256，防篡改
OperationID       string  // ★ 幂等键，跨重试保持不变
ReadOnly, Idempotent, Destructive, RequiresApproval bool  // 安全策略快照
```

### 索引

| 索引 | 列 | 服务于 |
|---|---|---|
| `idx_agent_task_poll` | (status, lease_until) | 过期租约回收 |
| `idx_agent_task_pending_scan` | (status, created_at, id) | pending 轮询（本次新增，见修复 4）|
| `idx_agent_task_owner_created` | (user_name, created_at) | 用户列表分页 |
| `idx_agent_step_sequence` | **UNIQUE** (task_id, sequence) | 防止步骤序号重复 |
| `idx_agent_step_task_status` | (task_id, status, created_at) | 恢复时查 running 步 |

---

## 2. 二十问逐条结论

| # | 问题 | 结论 |
|---|---|---|
| 1 | 有哪些表、字段、索引 | 2 张表，5 个复合索引，见上 |
| 2 | Checkpoint 独立建表？ | **内嵌 `agent_tasks.checkpoint`**，为了和状态转换同事务 |
| 3 | 存完整 Eino 状态？ | 存 Eino blob，但业务态被压到只剩 `GraphState{TaskID}` 一个字段 |
| 4 | 何时保存？ | **节点执行后**：`WithInterruptAfterNodes([plan, tool])` + 审批动态中断 |
| 5 | 重启后谁扫描？ | 每个实例都扫，无 leader。`ListPendingTasks` 只扫 `pending`；`RecoverStaleTasks` 扫 `running`+`planning` |
| 6 | 恢复粒度 | **步骤（step）边界**，节点内部不可恢复 |
| 7 | running 步恢复策略 | 看安全属性：plan/final/只读/幂等 → 重放；非幂等或破坏性 → `execution_unknown` |
| 8 | 如何跳过已完成步 | `nextRoute` 每次重读 MySQL，跳过 succeeded/rejected/skipped。**与 checkpoint 无关** |
| 9 | 多实例如何领取 | 单条原子 `UPDATE ... WHERE status='pending'`，`RowsAffected` 定胜负 |
| 10 | 有 lease_owner / lease_until / run_version 吗 | **三个全有** |
| 11 | fencing 如何防旧 Worker | **双重围栏**：`run_version = ?` **且** `lease_owner = ? AND lease_until > now` |
| 12 | checkpoint_version 参与 CAS？ | **不参与，只计数**。CAS 用的是 run_version |
| 13 | 审批如何中断/恢复 | 主动释放租约 → 动态中断落存档；批准后打回 pending → 重新 Claim |
| 14 | 审批绑定摘要/版本？ | ~~原本没有~~ → **已修复**，见第 4 节 |
| 15 | 取消 vs 审批 / 执行完成 | 全部正确。行锁串行化 + 终态 CAS，先到者赢 |
| 16 | 外部成功但未落库 | **收敛到 `execution_unknown`**，四条路径全覆盖 |
| 17 | 非幂等调用会自动重试吗 | **不会**，必须人显式 `retryUnknown=true` |
| 18 | resume vs 自动恢复 | 自动恢复处理「机器崩了」；resume 处理「需要人判断」 |
| 19 | 图升级后 checkpoint 兼容 | ~~原本有洞~~ → **已修复**，见第 4 节 |
| 20 | 有真实强杀测试吗 | **有**，`recovery_e2e_test.go` 真 `Process.Kill()`，覆盖幂等/非幂等两场景 |

---

## 3. 两个关键机制详解

### 3.1 「谁是权威」——MySQL 无条件胜出

担心的场景：*MySQL 把 running 步重置为 pending，但 Eino 还留着旧 Checkpoint，旧存档会不会让 Graph 跳过被重置的步骤？*

**不可能发生**，三层保证：

1. **物理上没有这个窗口** —— 重置步骤和清空 checkpoint 在**同一事务的同一条 UPDATE** 里（`dao/agent/agent.go:1064`）：

```go
tx.Model(&model.AgentTask{}).
  Where("id = ? AND status = ? AND run_version = ?", task.ID, task.Status, task.RunVersion).
  Updates(map[string]any{
      "status":             taskStatus,
      "checkpoint":         nil,                                  // ← 同一条语句
      "checkpoint_version": gorm.Expr("checkpoint_version + 1"),
      "run_version":        gorm.Expr("run_version + 1"),
  })
```

2. **就算存档还在，也跳不过步骤** —— checkpoint 里没有步骤进度，只有 `GraphState{TaskID}`。路由 100% 来自 MySQL 实时查询。**旧 checkpoint 物理上不具备「跳过步骤」的能力。**

3. **run_version 兜底** —— 旧 Worker 写任何东西都对不上号。

### 3.2 最危险崩溃窗口

```
t0  UpdateStepFenced(status=running, operation_id=X)  ← COMMIT，已落库
t1  mcp.Call(...)                                      ← ★ 副作用在这里发生
t2  UpdateStepFenced(status=succeeded, output=...)     ← 危险窗口 = [t1, t2]
```

窗口内出事的四条路径，**全部收敛到 `execution_unknown`**：

| 路径 | 触发 | 处理位置 |
|---|---|---|
| A | 进程被杀 | `RecoverStaleTasks` → `dao:1047` |
| B | Call 返回错误（含超时） | `failInvokedTool` → `graph.go:545` |
| C | Call 成功但写库失败 | `graph.go:402` 同样走 `failInvokedTool` |
| D | 连 unknown 都写不进去 | `worker.go:280` **故意什么都不做**，保持 running 等回收兜底 |

> 路径 D 的注释值得记住：*"never downgrade an uncertain side effect to ordinary failed/retryable"* —— **宁可卡住，绝不降级**。

---

## 4. 本次修复（2026-08-04）

### 修复 1：审批绑定参数摘要

**问题**：`ApproveTask` 只接受 `stepID`，不校验人看到的是什么参数。重放旧审批请求、或页面停留期间步骤变化，都可能批准到没审过的内容。

**修复**：

- API 强制要求 `expected_digest`，缺失 → `400`，对不上 → `409`
- 摘要进入 **store 层 CAS 谓词**（`dao:749`），不只是 service 前置校验
- **拒绝不要求摘要** —— 叫停任务必须能从过期页面发起，且拒绝永远安全

```go
// dao/agent/agent.go
if decision == model.AgentApprovalDecisionApproved && expectedDigest == "" {
    return nil, ErrInvalidInput
}
...
if expectedDigest != "" {
    stepQuery = stepQuery.Where("arguments_digest = ?", expectedDigest)
}
```

**API 变更**（破坏性）：

```
POST /agent/tasks/:taskID/steps/:stepID/approve
{"expected_digest": "a1b2c3..."}
```

**验证**：`TestGraphApprovalRequiresMatchingArgumentsDigest`。变异测试确认——摘掉 service 前置校验后测试仍通过，说明真正拦住的是 store 层 CAS。

### 修复 2：Checkpoint 版本信封

**问题**：卡在 `waiting_approval` 的任务不走清存档路径。图升级后批准 → 加载旧格式 checkpoint → 反序列化失败 → 任务莫名 `failed`，用户须再手动 resume 才自愈。

**修复**：给存储的 blob 加 schema 版本头，版本不匹配时 `Get` 返回「不存在」，让 Worker 直接走 `ForceNewRun`。因为跳过逻辑完全基于 MySQL，从头跑是安全的。

```go
const CheckpointSchemaVersion = "gopherai_task_agent_v1"
var checkpointEnvelopePrefix = []byte(CheckpointSchemaVersion + "\n")
```

图名也改用同一常量（`graph.go`），改图结构的人无法绕过版本号。旧的无信封 blob 自动被当作「不可解码」→ 平滑升级。

**验证**：`TestCheckpointEnvelopeIsolatesGraphVersions`（4 个子用例）。

### 修复 3：回收事务拆分

**问题**：`RecoverStaleTasks` 把**所有**僵死任务放在**一个事务**里 `FOR UPDATE`。100 个僵死任务 = 持有 100+ 行锁的长事务，且任一失败全批回滚。

**修复**：

- 先**无锁**扫描候选 ID，带 `LIMIT 50`
- 逐个在**独立事务**内加锁**复检**并恢复
- `ErrConflict` 视为正常竞争（别的 Worker 赢了），不算失败

### 修复 4：pending 扫描索引

**问题**：`ListPendingTasks` 是 `WHERE status='pending' ORDER BY created_at, id LIMIT n`，但唯一可用索引 `idx_agent_task_poll` 是 `(status, lease_until)` —— 只能满足等值谓词，排序退化成 filesort，每次轮询都要排全部 pending 行。

**修复**：`migrations/202608040002_agent_task_pending_scan_index.go` 新增 `(status, created_at, id)`，让 InnoDB 扫到 LIMIT 就能停。按本仓库惯例，refinement 索引只写在 migration 里，不进 model tag。

### 修复 5：崩溃恢复 E2E 接入 CI

**问题**：`recovery_e2e_test.go` 是「非幂等工具绝不自动重放」这条安全不变量的**唯一端到端证明**，却需要开发者手动设 DSN 才跑，必然腐烂。

**修复**：`.github/workflows/ci.yml` 新增 `agent-recovery-e2e` job，带 MySQL 8.0 service container，`-count=1` 关闭结果缓存（恢复行为依赖真实时序，缓存命中会让测试静默失效）。

> ⚠️ 本地无 Docker，该 job **尚未实跑验证**，仅验证了 YAML 可解析与 `go vet -tags integration` 通过。首次 CI 运行需要人盯一下。

---

## 5. 未修复项（已知，按优先级）

### 关于 #3 外部结果对账 —— 判断已修正

初版审计说「加 `Reconcile` 可把 `requires_review` 降一个数量级」，**这是高估，现予收回**。

读 `common/mcphub/redact.go:145` 的 `summarizeResult` 后确认：审计记录是**刻意做成无内容的**，只存 content 类型计数和字节数，不存实际工具输出。这是安全决策（审计表不得留凭据/原始输出）。因此即使给 `mcp_audit_records` 加上 `operation_id` 列：

- ❌ **不能自动补录步骤** —— 没有 output 可写回，`finalizeNode` 拿不到 `ResultSummary`
- ❌ **不能从「无审计记录」推断「没执行」** —— 审计写入靠 `Invoke` 的 `defer`，SIGKILL 时根本不会执行；缺记录 ≠ 没副作用
- ✅ **只能把「完全不知道」变成「确认执行过，request_id=X，耗时 340ms」** 给人看

即**纯信息价值，不改变任何自动决策**。收益远低于其成本（要动 model / 迁移 / registry / sink / gateway 接口五处），故暂缓。

真正解决这个问题的正确路径是**让工具自己支持 operation_id 幂等重放** —— 而这条链路代码已经打通（`graph.go:373` → `registry.go:543` 的 `_meta` 透传），工具只要如实声明 `Idempotent`，`stepCanReplayAfterCrash` 就会自动重放。所以缺的不是对账接口，是上游工具的幂等契约。

### 其余

| # | 问题 | 位置 | 建议 |
|---|---|---|---|
| 5 | `SetCheckpoint` 缺 lease 围栏（与其他写不对称）。审批节点故意先释放租约所致，代码已有注释说明取舍 | `dao:1201` | 可接受；固化行为的测试需要真实 DB，随 E2E 一起补 |
| 8 | 多 Worker 惊群：都拉同一批 pending 再抢 | `worker.go:91` | 量大时加分片或随机化。当前 `RowsAffected` 定胜负是正确的，只是浪费 |

**E2E 测试未覆盖场景**（建议补）：多 Worker 并发抢同一任务、崩溃发生在 `waiting_approval`、图升级后的 checkpoint 不兼容。

---

## 6. 总评

这套实现明显高于多数生产 Agent 框架，五个关键判断做对了：

1. **MySQL 单一权威 + checkpoint 可丢弃** —— 从根上消灭双权威冲突
2. **`GraphState` 只存 TaskID** —— 让上一条成为可能，是整个设计的支点
3. **`execution_unknown` 是一等公民状态** —— 多数框架只有 success/failed，把不确定性伪装成失败然后重试，这正是重复扣款的来源
4. **双重围栏（run_version + lease）** —— 堵住了单一 fencing token 的经典空窗
5. **「宁可卡住不降级」** —— 失败模式的品味
