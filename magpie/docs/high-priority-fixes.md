# API Router 高优先级问题与设计冲突修复指南

> **文档用途**：本文档梳理了 `api-router` 当前代码库中需优先解决的 **6 项高优先级程序 Bug 与设计冲突**，包含精确的代码定位、根因分析、现有单测兼容约束、推荐改法及验收命令。可直接将本文档交由新会话按序执行修复。

---

## 目录

1. [执行环境与全局约束](#一-执行环境与全局约束)
2. [P0 修复项 1：激活隐私脱敏的多轮会话缓存（Session Cache 死代码修复）](#二-p0-修复项-1激活隐私脱敏的多轮会话缓存session-cache-死代码修复)
3. [P0 修复项 2：修复非 `chat` 协议流式请求在本地执行时的无效推理与空文本误判](#三-p0-修复项-2修复非-chat-协议流式请求在本地执行时的无效推理与空文本误判)
4. [P0 修复项 3：修正 `routerledger.Store` 错误分级，防止单次参数/容量错误引发全局 503](#四-p0-修复项-3修正-routerledgerstore-错误分级防止单次参数容量错误引发全局-503)
5. [P0 修复项 4：清理 `credentials.json` 孤儿凭据，消除已删密钥残留与文件膨胀](#五-p0-修复项-4清理-credentialsjson-孤儿凭据消除已删密钥残留与文件膨胀)
6. [P1 修复项 5：对齐宿主接入边界（Claude Code `anthropic-beta` 拦截与跨协议宿主探针假阳性）](#六-p1-修复项-5对齐宿主接入边界claude-code-anthropic-beta-拦截与跨协议宿主探针假阳性)
7. [P1 修复项 6：分离 `routerruntime` 默认排队/推理预算 & 修复前端 `#redactionPanel` 面板隐藏逻辑](#七-p1-修复项-6分离-routerruntime-默认排队推理预算--修复前端-redactionpanel-面板隐藏逻辑)
8. [回归验证清单](#八-回归验证清单)

---

## 一、 执行环境与全局约束

1. **工作目录**：Go 模块根目录位于 [`magpie/`](file:///d:/api-router/magpie)（`go.mod` 所在目录）。
2. **安全与隐私红线**：
   - 严禁将任何 API Key、请求正文、响应正文或内部推理文本写入普通日志或未授权响应。
   - 保持 `config.json` 与 `credentials.json` 的分离存储契约。
   - 修改代码时务必对照各包现有 `*_test.go` 中的安全断言与边界测试。

---

## 二、 P0 修复项 1：激活隐私脱敏的多轮会话缓存（Session Cache 死代码修复）

### 1. 代码定位
- [`magpie/internal/routerservice/routing.go:1036`](file:///d:/api-router/magpie/internal/routerservice/routing.go#L1036)
- [`magpie/internal/routerredact/pipeline.go:167-210`](file:///d:/api-router/magpie/internal/routerredact/pipeline.go#L167-L210)

### 2. 问题根因
[`routerredact.Pipeline.Prepare`](file:///d:/api-router/magpie/internal/routerredact/pipeline.go#L167) 实现了基于历史消息前缀哈希的多轮对话脱敏缓存（`p.cache`），仅对新增轮次调用本地模型抽取实体，但要求：
```go
cacheable := in.SessionVerified && in.SessionID != ""
```
而在生产调用方 [`executeCloudModelSub`](file:///d:/api-router/magpie/internal/routerservice/routing.go#L1036) 中，传入的参数被硬编码为：
```go
preparedCloud, prepareErr = routing.Redactor.Prepare(redactionCtx, RedactionRequest{
    RequestID:       state.ID,
    AppID:           state.AppID,
    SourceProtocol:  protocol,
    TargetProtocol:  target.Provider.Config.Protocol,
    Operation:       operation,
    SessionID:       "",
    SessionVerified: false,
    Versions:        versions,
    Body:            wire.Body(),
    Context:         redactionCtx,
})
```
导致生产环境下 `cacheable` 恒为 `false`，`CacheHits` 永远为 `0`。多轮对话每增加一轮，都会将整段历史对话全量重新送入本地模型（1.7B/4B）抽取，耗时随轮数线性增长，极易触发 15s/30s 脱敏超时并导致 `503 redaction_unavailable`。

### 3. 兼容约束与安全边界
- 查看 [`magpie/internal/routerredact/pipeline_test.go`](file:///d:/api-router/magpie/internal/routerredact/pipeline_test.go)：缓存键由 `(AppID, SessionID, SourceProtocol, Versions)` 共同作用域隔离，且内部还会校验 `prefixHash`（历史消息字节级前缀指纹一致才命中）。
- 为防止跨应用污染，传入 `Prepare` 的 `SessionID` 必须绑定在当前 `state.AppID` 命名空间下。

### 4. 修复方案
1. 在 [`magpie/internal/routerservice/routing.go`](file:///d:/api-router/magpie/internal/routerservice/routing.go) 中新增受控的会话标识派生函数 `deriveRedactionSession(appID, protocol string, body []byte) (string, bool)`：
   - **优先提取宿主/协议内建会话标识**：如 JSON 顶层或 `metadata` 中的 `conversation_id` / `session_id` / `thread_id`，或首条用户/系统消息中的稳定 `traceId` / `messageId`（WorkBuddy 注入字段）。
   - **回退派生**：若请求中无显式会话 ID，但消息列表（`messages` 或 `input`）包含至少 1 条历史消息，可基于 `(appID, protocol, 首条系统消息 + 首条用户消息的 SHA-256 摘要)` 派生确定性的内部 `SessionID`（如 `"derived_" + hex[:32]`），并将 `SessionVerified` 设为 `true`（因为该 ID 由服务端根据应用与首轮消息内容确定性哈希生成，且 [`pipeline.go`](file:///d:/api-router/magpie/internal/routerredact/pipeline.go#L175-L185) 还会二次校验 `prefixHash`）。
2. 在 [`routing.go:1036`](file:///d:/api-router/magpie/internal/routerservice/routing.go#L1036) 将派生出的 `sessionID, sessionVerified` 传入 `RedactionRequest`。
3. 在 [`magpie/internal/routerservice/redaction_test.go`](file:///d:/api-router/magpie/internal/routerservice/redaction_test.go) 中增加端到端二轮对话测试，验证第二轮请求命中 `CacheHits >= 1` 且仅对新增消息发起本地抽取。

---

## 三、 P0 修复项 2：修复非 `chat` 协议流式请求在本地执行时的无效推理与空文本误判

### 1. 代码定位
- [`magpie/internal/routerservice/routing.go:333-377`](file:///d:/api-router/magpie/internal/routerservice/routing.go#L333-L377) (`writeLocalResponseObserved`)
- [`magpie/internal/routerservice/routing.go:668-713`](file:///d:/api-router/magpie/internal/routerservice/routing.go#L668-L713) (`localExecutionBody`)
- [`magpie/internal/routerservice/routing.go:841-909`](file:///d:/api-router/magpie/internal/routerservice/routing.go#L841-L909) (`DefaultLocalExecutor`)

### 2. 问题根因
1. **流式请求先跑完本地推理再丢弃回退**：
   - 当 `anthropic` 或 `responses` 协议客户端发送 `"stream": true` 请求且 `Judge` 返回 `"local"` 时，[`localExecutionBody`](file:///d:/api-router/magpie/internal/routerservice/routing.go#L688-L711) 调用 `codec.Request("chat", model, "")`，转换后的 Chat 请求**依然保留了 `"stream": true`**。
   - 本地 `llama-server` 完整执行流式生成并返回 `Content-Type: text/event-stream`。
   - 随后 [`writeLocalResponseObserved`](file:///d:/api-router/magpie/internal/routerservice/routing.go#L339-L344) 检测到 `stream && protocol != "chat"`，将 `response.Body` 读空丢弃，返回 `local_protocol_stream_conversion_unavailable`，再回退到云端执行。这导致每次非 `chat` 流式请求命中本地时都白白浪费一次完整的本地推理耗时并阻塞唯一槽位。
2. **合法空文本响应被误判为协议转换失败**：
   - 在 [`routing.go:359-362`](file:///d:/api-router/magpie/internal/routerservice/routing.go#L359-L362) 中：
     ```go
     reply, err := parseLocalReply(body)
     if err != nil || (reply.Text == "" && len(reply.ToolCalls) == 0) {
         return errors.New("local_protocol_conversion_unavailable")
     }
     ```
   - 若 `parseLocalReply` 解析成功（合法的 Chat Completion 响应，包含 `choices`），但因 `finish_reason: "length"` 或停止词导致 `reply.Text == ""` 且无工具调用，不应视作协议转换错误。

### 3. 兼容约束
- 必须先检查 [`magpie/internal/routerservice/routing_test.go`](file:///d:/api-router/magpie/internal/routerservice/routing_test.go) 中是否有针对 `local_protocol_stream_conversion_unavailable` 的既有测试用例！
  - 如果现有测试明确断言：`anthropic` / `responses` 在 `stream: true` 时不支持本地流式转换、应当回退云端（`FallbackReason: "local_protocol_stream_conversion_unavailable"`），那么应当在 **`localExecutionBody`（调用本地 `llama-server` 之前）** 或 `DefaultLocalExecutor` 发起 HTTP 请求前直接检测 `protocol != "chat" && isStreamRequest(source)` 并立即返回 `errors.New("local_protocol_stream_conversion_unavailable")`（或者通过 `cloudcodec` 支持将本地流式响应转换为目标协议 SSE）。
  - **关键点**：在进入 `llama-server` 推理前拦截（或直接用 `cloudcodec` 完成流式转换），即可避免占用本地槽位空跑推理！

### 4. 修复方案
1. 先阅读 [`magpie/internal/routerservice/routing_test.go`](file:///d:/api-router/magpie/internal/routerservice/routing_test.go) 中搜索 `local_protocol_stream_conversion_unavailable` 和 `parseLocalReply` 的相关测试。
2. 在 [`localExecutionBody`](file:///d:/api-router/magpie/internal/routerservice/routing.go#L668-L713) 中（或发起 `llama-server` HTTP 调用前）：
   - 若保持“非 `chat` 协议流式请求回退云端”的语义，则在 `localExecutionBody` 开头判断 `if protocol != "chat" && isStreamRequest(source)`，直接返回 `nil, errors.New("local_protocol_stream_conversion_unavailable")`，从而在耗时推理发生前零成本触发云端回退；保留 `writeLocalResponseObserved` 中的兜底检查以防本地服务自行返回 `text/event-stream`。
3. 在 [`parseLocalReply`](file:///d:/api-router/magpie/internal/routerservice/routing.go) 与 [`writeLocalResponseObserved`](file:///d:/api-router/magpie/internal/routerservice/routing.go#L359-L362) 中：
   - 令 `parseLocalReply` 严格校验 JSON 结构是否包含合法的 `choices` 数组（若无合法 `choices` 或 `message` 结构则返回 `err != nil`），而在结构合法时允许 `reply.Text == ""` 正常构造 `anthropic` / `responses` 响应。

---

## 四、 P0 修复项 3：修正 `routerledger.Store` 错误分级，防止单次参数/容量错误引发全局 503

### 1. 代码定位
- [`magpie/internal/routerledger/store.go:294-305`](file:///d:/api-router/magpie/internal/routerledger/store.go#L294-L305)
- [`magpie/internal/routerledger/store.go:316-415`](file:///d:/api-router/magpie/internal/routerledger/store.go#L316-L415)
- [`magpie/internal/routerservice/requests.go:122-125`](file:///d:/api-router/magpie/internal/routerservice/requests.go#L122-L125)

### 2. 问题根因
在 [`store.go:294-300`](file:///d:/api-router/magpie/internal/routerledger/store.go#L294-L300) 中：
```go
func (s *Store) failure(err error) error {
	s.status.Available = false
	s.status.ErrorCode = err.Error()
	s.status.LastErrorCode = err.Error()
	s.status.FailureCount++
	return err
}
```
在 `Start(m)` 和 `Finish(id, o)` 中，当遇到单次请求的参数校验错误（`ErrInvalid`，如 `AcceptedAt.After(now)`）或活跃请求占满容量（`ErrCapacity`）时，都会调用 `s.failure(err)`，导致 `s.status.Available` 被设为 `false`。
而在 [`requests.go:122`](file:///d:/api-router/magpie/internal/routerservice/requests.go#L122) 中：
```go
if ledger == nil || !status.Available {
	fail(503, "ledger_unavailable", "request ledger unavailable")
	return
}
```
这意味着只要有一次 `Start`/`Finish` 返回 `ErrInvalid` 或 `ErrCapacity`，整个 `/api/requests` 与 `/api/requests/summary` 接口就会持续返回 `503 ledger_unavailable`，直到下一次有请求成功 `Start` 或 `Finish`。

### 3. 兼容约束（重要！）
请务必注意 [`magpie/internal/routerledger/store_test.go`](file:///d:/api-router/magpie/internal/routerledger/store_test.go) 中的两处现有断言：
1. [`store_test.go:251-256`](file:///d:/api-router/magpie/internal/routerledger/store_test.go#L251-L256)：
   ```go
   if err := s.Start(metadata("overflow")); !errors.Is(err, ErrCapacity) {
       t.Fatalf("active capacity=%v", err)
   }
   if s.Status().ErrorCode != ErrCapacity.Error() {
       t.Fatal("admission failure was not diagnosable")
   }
   ```
   **约束**：当发生 `ErrCapacity`（或 `ErrInvalid`）时，`s.Status().ErrorCode`、`s.Status().LastErrorCode` 和 `s.Status().FailureCount` **仍然必须更新**！
2. [`store_test.go:462-467`](file:///d:/api-router/magpie/internal/routerledger/store_test.go#L462-L467)：
   ```go
   if err := s.Finish("disk-failure", success()); !errors.Is(err, ErrUnavailable) {
       t.Fatalf("failed write=%v", err)
   }
   if s.Status().Available || s.Status().ErrorCode != ErrUnavailable.Error() || get(t, s, "disk-failure").Status != "processing" {
       t.Fatal("write failure masqueraded as persisted success")
   }
   ```
   **约束**：当发生真正的存储/数据库级错误（`ErrUnavailable`、`ErrCorrupt`、`ErrSchemaIncompatible`）时，`s.Status().Available` **必须设为 `false`**！

### 4. 修复方案
在 [`magpie/internal/routerledger/store.go:294-300`](file:///d:/api-router/magpie/internal/routerledger/store.go#L294-L300) 中修改 `failure(err error)`：
```go
func (s *Store) failure(err error) error {
	if !errors.Is(err, ErrInvalid) && !errors.Is(err, ErrCapacity) && !errors.Is(err, ErrConflict) && !errors.Is(err, ErrNotFound) {
		s.status.Available = false
	}
	s.status.ErrorCode = err.Error()
	s.status.LastErrorCode = err.Error()
	s.status.FailureCount++
	return err
}
```
这样既保留了 `ErrorCode`、`LastErrorCode`、`FailureCount` 的可诊断性（100% 兼容 `store_test.go:254`），又确保只有真正的 SQLite 存储级故障才会将 `s.status.Available` 置为 `false`（100% 兼容 `store_test.go:465`），彻底消除单次参数/容量错误导致 `/api/requests` 503 瘫痪的问题。

---

## 五、 P0 修复项 4：清理 `credentials.json` 孤儿凭据，消除已删密钥残留与文件膨胀

### 1. 代码定位
- [`magpie/internal/routerconfig/providers.go:341-389`](file:///d:/api-router/magpie/internal/routerconfig/providers.go#L341-L389) (`SaveCloudProviderContext`)
- [`magpie/internal/routerconfig/providers.go:395-434`](file:///d:/api-router/magpie/internal/routerconfig/providers.go#L395-L434) (`DeleteCloudProviderContext`)
- [`magpie/internal/routerconfig/providers.go:436-491`](file:///d:/api-router/magpie/internal/routerconfig/providers.go#L436-L491) (`writeProviders`)

### 2. 问题根因
1. `SaveCloudProviderContext` 每次更新供应商都会生成新的 `cred_<random>` 并写入 `f.secrets[ref]`，但从不清理历史 `credentialRef`。
2. `DeleteCloudProviderContext` 删除供应商时仅调用 `delete(f.providers, id)` 并传入 `credentialsChanged = false`（[`providers.go:432-433`](file:///d:/api-router/magpie/internal/routerconfig/providers.go#L432-L433)），从未删除 `credentials.json` 中对应的密钥条目。

### 3. 兼容约束（极其重要！）
查看 [`magpie/internal/routerconfig/providers_test.go:76-91`](file:///d:/api-router/magpie/internal/routerconfig/providers_test.go#L76-L91)：
```go
	var old Document
	oldBytes, err := os.ReadFile(s.layout.ConfigFile() + ".bak")
	...
	var oldRef string
	_ = json.Unmarshal(oldProviders[saved.ID]["credentialRef"], &oldRef)
	f, err := s.providerFiles()
	...
	if !bytes.Contains(f.secrets[oldRef], []byte(*in.APIKey)) {
		t.Fatal("backup credential reference was destroyed")
	}
```
**设计意图**：`config.json.bak` 是 `steady.WriteBytes` 生成的**上一版本**配置文件备份。如果用户刚改完配置需要从 `config.json.bak` 恢复，`config.json.bak` 中各个供应商引用的 `credentialRef` 必须仍然存在于 `credentials.json` 中！
因此：
- **在 `SaveCloudProviderContext` 更新供应商时**：应当保留**当前 `f.providers`** 以及**即将成为 `config.json.bak` 的修改前 `config.json`（即本次事务开始前磁盘上的 `config.json`）** 中所引用的全部 `credentialRef`，而将更早之前的孤儿 `cred_*` 全部从 `f.secrets` 中清理掉！
- **在 `DeleteCloudProviderContext` 删除供应商时**：如果希望彻底清除被删供应商的凭据（或仅保留仍在 `f.providers` / 备份中引用的凭据），需注意检查 `providers_test.go` 是否对删除后的 `.bak` 有断言（[`TestProviderDeleteAndReferences`](file:///d:/api-router/magpie/internal/routerconfig/providers_test.go#L201-L232) 未断言已删供应商的 `.bak` 凭据保留）。若统一采用“保留当前 `f.providers` + 本次写入前 `config.json`（即写入后的 `config.json.bak`）引用的 `credentialRef`”，或者在 `DeleteCloudProviderContext` 中直接删除被删供应商的 `credentialRef` 并传 `credentialsChanged = true`，需确保所有现有单测通过。

### 4. 修复方案
1. 在 [`providers.go`](file:///d:/api-router/magpie/internal/routerconfig/providers.go) 中新增辅助函数 `pruneOrphanSecrets(f *providerFiles, retainedRefs map[string]bool)`：
   - 收集当前最新 `f.providers` 中所有供应商的 `credentialRef`。
   - 收集本次写入后将成为 `config.json.bak` 的前置活跃供应商集合中的 `credentialRef`（对于 `DeleteCloudProviderContext`，将被显式删除的供应商 ID 的 `credentialRef` 排除在外，以保证删除供应商后其密钥立即从 `credentials.json` 抹除）。
   - 遍历 `f.secrets`，删除不在 `retainedRefs` 中的所有孤儿 key。
2. 在 [`DeleteCloudProviderContext`](file:///d:/api-router/magpie/internal/routerconfig/providers.go#L432-L433) 中：
   - 删除 `f.providers[id]` 的同时，从 `f.secrets` 中删除该供应商关联的 `credentialRef`（以及任何其他孤儿 `cred_*`），并将 `s.writeProviders(ctx, f, true)` 的第三个参数改为 `true` 以持久化更新 `credentials.json`。
3. 在 [`providers_test.go`](file:///d:/api-router/magpie/internal/routerconfig/providers_test.go) 中补充断言：
   - 连续保存同一供应商 3 次后，`f.secrets` 中最多只保留当前版本与 `.bak` 版本的 2 个 `credentialRef`（第 1 次的旧密钥已被清理）；
   - 调用 `DeleteCloudProvider` 后，`credentials.json` 中不再包含被删供应商的 API Key。

---

## 六、 P1 修复项 5：对齐宿主接入边界（Claude Code `anthropic-beta` 拦截与跨协议宿主探针假阳性）

### 1. 代码定位
- [`magpie/internal/routerservice/models.go:280-292`](file:///d:/api-router/magpie/internal/routerservice/models.go#L280-L292) (`rejectUnsupportedModelHeaders`)
- [`magpie/internal/routerservice/models_test.go`](file:///d:/api-router/magpie/internal/routerservice/models_test.go)
- [`magpie/internal/routerservice/apps.go:411-441`](file:///d:/api-router/magpie/internal/routerservice/apps.go#L411-L441) (`probeAppCloud`)

### 2. 问题根因
1. **Claude Code 真实请求携带 `anthropic-beta` 被网关拦截**：
   - [`rejectUnsupportedModelHeaders`](file:///d:/api-router/magpie/internal/routerservice/models.go#L280-L292) 对任何非空的 `anthropic-beta` 或 `OpenAI-Beta` 请求头直接报错拒绝（`400 unsupported_input`）。
   - 但真实的 Claude Code CLI 在请求 `/v1/messages` 时会自动附带 `anthropic-beta` 头，导致 `api-router host apply --host claude-code` 探针成功，而实际 CLI 对话全部 400 失败。
2. **WorkBuddy / Loomy 跨协议绑定供应商时，合成探针假阳性**：
   - WorkBuddy（`chat` 协议）真实请求包含消息级扩展字段 `agent`、`messageId`、`traceId`；Loomy（`chat` 协议）包含顶层 `$comment`。
   - [`cloudcodec`](file:///d:/api-router/magpie/internal/cloudcodec) 在同协议（`chat` -> `chat`）下允许透传这些字段，但在跨协议（如 WorkBuddy 绑定到 `anthropic` 或 `responses` 供应商）下会拒绝并报错 `400 capability_message_agent` / `capability_message_metadata`。
   - 然而 [`apps.go:411-441`](file:///d:/api-router/magpie/internal/routerservice/apps.go#L411-L441) 的 `/api/apps/check` 与 [`routerhost/engine.go:619-675`](file:///d:/api-router/magpie/internal/routerhost/engine.go#L619-L675) 的探针请求体过于简单，未包含宿主特征字段，导致跨协议绑定时探针返回“已验证”，实际使用却立即报错。

### 3. 兼容约束
- 先检查 [`magpie/internal/routerservice/models_test.go`](file:///d:/api-router/magpie/internal/routerservice/models_test.go) 和 [`magpie/internal/routercloud/cloud_test.go`](file:///d:/api-router/magpie/internal/routercloud/cloud_test.go) 中是否有测试专门断言“携带未知 `anthropic-beta` 或 `OpenAI-Beta` 必须返回 400 `unsupported_input`”。
- 若存在该契约测试，则需区分：
  - 要么针对已知宿主允许/剥离客户端侧无害的 `anthropic-beta` 值（或仅在跨协议转换无法表达时拦截，同协议 `anthropic` -> `anthropic` 透传或安全忽略）；
  - 要么按测试契约更新对应规则与测试。

### 4. 修复方案
1. **检查并优化 `anthropic-beta` 处理**（[`models.go:280-292`](file:///d:/api-router/magpie/internal/routerservice/models.go#L280-L292)）：
   - 先查阅 `models_test.go` 中关于 `anthropic-beta` 的测试断言。
   - 若测试要求默认拦截未知 beta header，可对 Claude Code 常见客户端 beta 标识（如 `claude-code-*`、`token-counting-*` 等不改变请求体必选语义的标记）在入站预检时剥离/忽略，或评估是否允许 `claude-code` 应用入站时剥离 `anthropic-beta` 而不报 400。
2. **增强宿主探针报文真实性**（[`apps.go:411-441`](file:///d:/api-router/magpie/internal/routerservice/apps.go#L411-L441) 与 [`routerhost/engine.go:640-675`](file:///d:/api-router/magpie/internal/routerhost/engine.go#L640-L675)）：
   - 在 `probeBody(appID, protocol)` 构造探针请求体时，若 `appID == "workbuddy"` 或 `appID == "loomy"`，或当应用绑定了跨协议供应商时，在探针或预检阶段校验宿主扩展字段的跨协议兼容性（或在 `cloudcodec` 中对纯展示/追踪性质的客户端元数据如 `messageId`、`traceId`、`$comment` 在跨协议转换时安全剥离而非报错阻断，同时注意核对 `cloudcodec_test.go` 的既有断言）。

---

## 七、 P1 修复项 6：分离 `routerruntime` 默认排队/推理预算 & 修复前端 `#redactionPanel` 面板隐藏逻辑

### 1. 子项 A：`routerruntime.Manager.Use` 默认预算分离
- **代码定位**：[`magpie/internal/routerruntime/manager.go:700-748`](file:///d:/api-router/magpie/internal/routerruntime/manager.go#L700-L748)
- **问题根因**：
  在 [`manager.go:738-748`](file:///d:/api-router/magpie/internal/routerruntime/manager.go#L738-L748) 中：
  ```go
  if p == Execution {
      executionBudget := m.o.ExecutionBudget
      if !settings.IsZero() {
          executionBudget = time.Duration(settings.ExecutionSeconds) * time.Second
      }
      pctx, finish = context.WithTimeout(ctx, executionBudget)
  } else if p == Judgment && !settings.IsZero() {
      pctx, finish = context.WithTimeout(ctx, time.Duration(settings.JudgmentInferenceSeconds)*time.Second)
  } else {
      pctx, finish = context.WithCancel(stage)
  }
  ```
  当 `settings.IsZero()` 为 `true` 时，`Judgment` 和 `Redaction` 直接复用了排队阶段的 `stage` context，导致排队等待耗时直接扣减推理预算，且超时后在 770 行被 `purposeTimeout(ctx, p, false)` 报告为推理超时。
- **兼容约束**：
  检查 [`magpie/internal/routerruntime/manager_test.go:761-780`](file:///d:/api-router/magpie/internal/routerruntime/manager_test.go#L761-L780) (`TestCallerCancellationAndPurposeDeadlinesRemainDistinctFromCrash`)，该测试设置了 `o.JudgmentBudget = 20 * time.Millisecond`、`o.RedactionBudget = 20 * time.Millisecond` 并期望 `fn` 内部 `<-ctx.Done()` 在 20ms 后触发 `context.DeadlineExceeded`。
- **修复方案**：
  在 `else` 分支（即 `p == Judgment` 或 `p == Redaction` 获得槽位开始执行 `fn` 时），使用对应的 `budget`（`m.o.JudgmentBudget` 或 `m.o.RedactionBudget`）基于父级 `ctx` 重新创建独立的推理超时 `pctx, finish = context.WithTimeout(ctx, budget)`，使排队预算（`stage`）与推理预算（`pctx`）彻底解耦，同时完全兼容 `TestCallerCancellationAndPurposeDeadlinesRemainDistinctFromCrash`！

### 2. 子项 B：前端 `#redactionPanel` 状态面板显示条件修复
- **代码定位**：[`magpie/internal/routerservice/assets/redaction.js:50-52`](file:///d:/api-router/magpie/internal/routerservice/assets/redaction.js#L50-L52)
- **问题根因**：
  ```javascript
  const enabled = !!managementToken && !!data?.intents?.some(p => p.enabled);
  $('#redactionPanel').hidden = !enabled;
  ```
  当还没有任何应用开启 `redactionEnabled` 时，即使管理员以 `--experimental-redaction` 启动了服务，或者 `/api/redaction` 返回了重要的状态诊断信息（如 `status`、`reason`、`stats`），`#redactionPanel` 也会被隐藏。
- **兼容约束**：
  检查 [`magpie/scripts/ui-redaction-regression.cjs`](file:///d:/api-router/magpie/scripts/ui-redaction-regression.cjs) 中的 Playwright 断言，确认测试是否对 `#redactionPanel` 的默认 `hidden` 状态有特定预期！
  - 若 `ui-redaction-regression.cjs` 期望在未开启 `--experimental-redaction` 且无应用启用脱敏时隐藏 `#redactionPanel`，则可将条件改为：
    ```javascript
    const enabled = !!managementToken && (!!data?.intents?.some(p => p.enabled) || data?.status === 'ready' || (data?.reason && data.reason !== 'experimental_disabled'));
    ```
  - 请以 `magpie/scripts/ui-redaction-regression.cjs` 的实际断言为准进行对齐。

---

## 八、 回归验证清单

每次完成一个修复项后，请在 `d:\api-router\magpie` 目录下执行对应的单元测试，并在全部完成后运行完整回归：

1. **配置与凭据回收测试**：
   ```powershell
   go test -v ./internal/routerconfig
   ```
2. **SQLite 账本与错误分级测试**：
   ```powershell
   go test -v ./internal/routerledger
   ```
3. **本地模型运行时与预算测试**：
   ```powershell
   go test -v ./internal/routerruntime ./internal/routerprocess
   ```
4. **隐私脱敏与路由服务测试**：
   ```powershell
   go test -v ./internal/routerredact ./internal/routerservice
   ```
5. **全量 Go 单元测试（26 个包）**：
   ```powershell
   go test ./...
   ```
6. **全量产品级回归测试（含 Playwright 浏览器套件）**：
   ```powershell
   node ./scripts/product-regression.cjs
   ```
