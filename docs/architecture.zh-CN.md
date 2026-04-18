# Loom Gateway 架构

## 项目定位

`loom-cli` 不是 API 网关，而是一个**面向 AI skills 的确定性治理平面**：把所有外部意图 —— 来自 agent、markdown skill、MCP 调用，或未来的协议 —— 视为不可信输入，强制穿过一条"信任不断收窄"的分层边界链，才允许产生任何真实副作用。

项目的根本赌注是：**skills 和 prompts 是长期资产，agent 和 LLM 频繁更替**。因此 IR（内部表示）是系统稳定的核心；ingress 适配器和 LLM 侧工具是可替换的边缘。

让这件事成立的设计铁律：

> 每一层都假设上一层可能失败。

这不是"纵深防御"的口号，而是一个具体的工程原则：没有任何单一组件 —— 不是 parser、不是 validator、不是 sanitizer，甚至不是影子文件系统 —— 被当作最后一道防线。

## 核心设计理念

五条原则贯穿每个模块。它们是承重设计，违反任何一条都会让安全叙事破产。

### 1. 类型歧义就是安全歧义（Typed ambiguity becomes security ambiguity）

IR 内部绝不使用 `any`、`map[string]interface{}` 或自由字符串承载语义。`Step.Action string` 会迫使 validator 用正则去猜意图；`Spec map[string]any` 只是把同样的无类型 payload 下沉一层。两者都模糊了"合法"与"安全"的边界。IR 采用类型化 tagged union（`StepKind` + `StepArgs` 接口 + 每种 Kind 专属结构体），让每一个 validator/executor 分支都在具体类型上工作，而不是在一段字符串的解析结果上。

### 2. 能力必须带 Scope（Capabilities carry scope）

裸露的能力名（比如 "vfs.write"）等于无界授权。IR 中每个 `Capability` 都由 `Kind` + `Scope` 构成（例如 `vfs.write` + `out/`）。声明的能力是**有效能力的上界** —— validator 从每个 Step 的 typed Args 推导出派生能力集，如果任何派生 scope 无法被某条同类型声明覆盖（前缀包含），就拒绝该 skill。声明只能缩小，永远不能扩权。

### 3. 先准入，后执行（Admission before execution）

任何能改变状态的事情，都必须在准入决策封口之后才执行。Validation、sanitization、能力天花板检查全部完成，并且写入带有 canonical logical hash 的 Receipt 之后，executor 才会分发第一个 step。准入把"意图"转化为一个可审计的事实。

### 4. 先隔离，后变更（Isolation before mutation）

任何执行路径都不能变更真实工作区。所有写入通过 `ShadowVFS` 落入隔离的影子目录。**commit gate** 是一道独立边界，它读取影子 manifest —— 当前 spike 阶段只打印。把字节推进到真实工作区是一个显式的、独立的动作，而不是"skill 跑完了"的副作用。

### 5. 核心稳定，边缘可替换（Stable core, replaceable edges）

IR 是稳定的契约。Parsers、agents、协议适配器都在边缘，它们翻译并交付 IR。新的 ingress 格式加一个 parser；不动核心类型。新的 step verb 向 `argsRegistry` 注册；不动 hash 函数，也不动任何中心 dispatch switch。这就是系统能在 agent 生态高速演进下，不让核心层频繁翻修的原因。

## 分层信任收窄

执行链是一串信任不断收窄的阶段。每一阶段从更低信任的区域取入，输出下一阶段可以稍微更信任一点的东西。端到端没有完全可信的东西。

1. **协议入口（Protocol ingress）** —— [`cmd/main.go`](../cmd/main.go) 提供 CLI，[`cmd/serve.go`](../cmd/serve.go) 提供 MCP sidecar。未来可扩展 webhook、git hook 等。
2. **Parser 前端** —— [`internal/engine/parser`](../internal/engine/parser)。OpenClaw markdown（v0 legacy）与 V1 JSON（`.loom.json`）。[`parser.go`](../internal/engine/parser/parser.go) 中的 router 按扩展名分发。不可信外部格式在此被转换成类型化的 `LoomSkill`。
3. **类型化语义 IR** —— [`internal/engine/ir.go`](../internal/engine/ir.go)。`SchemaVersion`、`StepKind`、`StepArgs`、`Capability{Kind, Scope}`。Canonical logical hash 把运行时行为绑定到已审计的结构上；`SchemaVersion` 是第一个写入 hasher 的字节，从而保证"仅版本不同的两个 skill 永不碰撞"。
4. **静态 validator** —— [`internal/engine/validator.go`](../internal/engine/validator.go)。数据流完整性、能力天花板、危险命令扫描、SSRF 扫描（后两条仍覆盖 v0 legacy steps 和 v1 args 中出现的任何静态字符串）。
5. **输入 sanitizer** —— [`internal/engine/sanitizer.go`](../internal/engine/sanitizer.go)。类型强制、必填/默认语义、shell 注入标记、以及 `SanitizeShadowRelPath` 做路径范围检查。Sanitizer 是纵深防御的第二道线：路径越界通常会先被 validator 的能力天花板检查拦住，但 sanitizer 永远不会被跳过。
6. **影子工作区隔离（Shadow workspace isolation）** —— [`internal/engine/vfs.go`](../internal/engine/vfs.go)。所有写入都经由 `ShadowVFS.ResolveWritePath` 解析。删除用 tombstone 记录。在 commit gate 显式批准之前，真实工作区是只读基线。
7. **Compiler / admission controller** —— [`internal/engine/compiler.go`](../internal/engine/compiler.go)。编排 validate → sanitize → 分配影子目录 → 写 receipt。Receipt 包含 `SchemaVersion`、`LogicalHash`、授予的 `Capabilities`、影子路径。
8. **Executor** —— [`internal/engine/executor.go`](../internal/engine/executor.go)。v1 spike 中提供 `read_file` 和 `write_file` 的类型分发 handler。所有 I/O 都经 `ShadowVFS`。**原子化失败语义**：任一 step 出错即中止，影子永不被提升。
9. **Commit gate** —— [`internal/engine/commit_gate.go`](../internal/engine/commit_gate.go)。当前形态：读取 `ShadowVFS.Manifest()` 并打印。未来形态：diff、冲突检测、审批，并且是通往真实工作区变更的唯一路径。

## 信任边界

每条边界都是某一类失败被限定的地方。

- **B1：外部语法 → 内部 IR。** Markdown、JSON、agent 文本都不是执行意图。只有成功解析出的 `LoomSkill` 才越过这条边界。
- **B2：IR → 可准入 skill。** 语法合法不代表语义安全。结构化数据流、能力天花板、静态规则必须全部通过。
- **B3：原始调用输入 → 类型化运行时值。** `SanitizeInput` 拒绝未声明参数、非法类型强制、shell 标记。`SanitizeShadowRelPath` 拒绝路径越界与根路径。
- **B4：逻辑批准 → 文件系统副作用。** 即便 sanitize 通过，仍然不能触碰真实工作区。`ShadowVFS` 是**物理隔离**边界。
- **B5：影子完成 → 真实变更。** 这条边界由 commit gate 独占。当前 spike 阶段是无法越过的 —— 这是设计使然：promotion 暂未实现。

## 为什么这是纵深防御

每一层都防御一类不同的失败，并且下一层假设上一层可能失败：

- 如果 parser 把一段文本分类错了，validator 仍应拒绝未声明的依赖、未覆盖的能力、或静态危险内容。
- 如果 validator 漏了一种情况，sanitizer 仍应拒绝敌意运行时字符串与路径越界。
- 如果 sanitizer 漏了一种情况，`ShadowVFS` 仍应阻止对真实工作区的变更。
- 如果执行过程中产生了危险状态，commit gate 仍应阻止其被提升。
- 如果攻击者绕过了准入，Receipt 的 logical hash 仍应在事后暴露行为漂移。

任何单层都不被期望是完美的。

## IR 版本契约

IR 带有显式的 `SchemaVersion`。`CurrentSchemaVersion = "v1"` 是 executor 唯一接受的版本。

- **v0**（`SchemaVersion` 为空） —— 由 OpenClaw markdown parser 产生。指令文本是自然语言，无法机械推断出类型化 Kind。v0 skill 仍然可以被 parse 与 `verify`，但 **executor 会拒绝运行** 并给出清晰的错误。系统不会试图自动升级 v0 —— 语义升级需要 parser 无法安全完成的推理。
- **v1** —— 以 `.loom.json` 形式编写。每个 Step 都有 `Kind` + 类型化 `Args`；每条 capability 都带 scope。端到端可执行。

未来版本通过在 `argsRegistry` 注册新 step kind 来扩展契约；如果 IR 形状本身需要变更，则递增 `SchemaVersion`，并由 parser 负责把老版本文档升级到当前版本后再交给 IR 层。**IR 层永不处理历史包袱，这严格是 parser 的职责。**

## 当前状态（Core Spike）

已交付：
- v1 类型化 IR + 流式 canonical hash
- 带 scope 的能力模型 + 天花板强制
- `read_file` / `write_file` 的 executor，全部走 `ShadowVFS`
- 打印影子 manifest 的 commit gate（不执行 promotion）
- v1 JSON parser 前端；v0 markdown parser 保留用于兼容
- 端到端集成测试：验证四条沙箱不变量

明确的 Out of Scope：
- `os_command` / `http_call` 两类 step kind
- 真正的 commit/promote（影子 → 工作区）
- MCP executor 集成（sidecar 仍保持拦截-only）
- 以 logical hash 为键的 admission 缓存
- 除 `argsRegistry` 之外的插件注册表

## 路线图

路线图由 spike 暴露出的真实问题塑造，而不是纸上推理。

### Phase A —— 强化隔离与提升路径
- 将 `ShadowVFS` 的删除语义暴露给 v1 step kinds
- 利用 shadow manifest 做 diff 与冲突检测
- commit gate 引入带显式审批的 promotion 路径

### Phase B —— 扩大 executor 表面
- `os_command`：argv 类型化参数（拒绝 shell 字符串），附沙箱 profile
- `http_call`：由 capabilities 组合出 URL allowlist
- 所有 I/O 边界强制应用 `RedactOutput`

### Phase C —— 权限从声明转为派生
- 在可能的地方从 Kind + Args 完整派生有效能力，声明退化为"只可缩小的天花板"
- 拆分"admission hash"与"execution hash"，让运行时计算的内容不再动摇准入指纹

### Phase D —— 运维级 commit gate
- 影子 diff 渲染
- 显式审批的变更 review
- 审计 manifest 随 Receipt 一并留存

## 小结

`loom-cli` 是一个**分层的确定性 skill 治理平面**：

- 多种 ingress 形态 → 翻译为同一套类型化 IR
- 多个职责不重叠的执行前防御层
- 已批准意图与真实变更之间的物理隔离
- 可审计的准入决策，其 hash 把行为绑定到结构上

核心小而稳定，并且对"歧义"敌对；边缘吸收 agent 生态的高速演进。**这种分离，就是项目的架构身份。**
