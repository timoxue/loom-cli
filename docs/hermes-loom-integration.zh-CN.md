# Hermes × Loom：带安全闸门的自进化 Agent

## Hermes 是什么

Hermes 是一个 AI agent 编排框架（Harness）。它的职责是：构建系统提示词、管理记忆、分发工具调用、持久化会话历史。Skill（OpenClaw 格式的 `SKILL.md` 文件）是它的"程序性记忆"单元——告诉 agent 如何处理某一类任务。

核心入口：
- `run_agent.py` — `AIAgent` 主循环：系统提示组装 → LLM 调用 → 工具分发 → 循环
- `tools/skill_manager_tool.py` — Agent 驱动的 skill 生命周期（创建 / 编辑 / 打补丁）
- `tools/mcp_tool.py` — MCP 客户端集成（1050+ 行；支持 stdio + HTTP 传输）
- `hermes_cli/claw.py` — `hermes claw migrate` OpenClaw 导入命令

---

## Hermes 的自进化机制

Hermes 可以跨会话持续改进自身，机制分三层：

### 第一层：Skill 创建（程序性记忆）

当 agent 成功完成一个新类型的任务后，它可以将这段经验永久编码：

```python
skill_manage(action="create", name="deseq2-workflow", category="bioinformatics",
             content="---\nname: deseq2-workflow\n...\n---\n## 如何运行\n...")
```

- 写入 `~/.hermes/skills/bioinformatics/deseq2-workflow/SKILL.md`
- 写入后立即执行 `skills_guard.py` 安全扫描
- **没有人工审核门** — 扫描通过后，skill 在下次会话即可使用
- Skill 作为用户消息注入 LLM 上下文（而非系统提示词），以保留提示词缓存

### 第二层：Skill 改进（打补丁 / 全量编辑）

Skill 可根据经验原地更新：

```python
skill_manage(action="patch", name="deseq2-workflow",
             old_content="--num_recycles 3",
             new_content="--num_recycles 5  # 多聚体精度更好")
```

`fuzzy_match.py` 处理空白字符和缩进漂移。全量重写使用 `action="edit"`。所有写操作均为原子操作，扫描失败时自动回滚。

### 第三层：跨会话用户建模（Honcho）

`optional-skills/autonomous-ai-agents/honcho/` 提供多轮辩证反思：
- 跨会话观察行为模式
- 调优记忆召回和摘要
- 用户模型存储在 `~/.hermes/memories/USER.md`

---

## Hermes 如何调用外部工具

### 内置工具注册表

`tools/registry.py` 是单例。每个 `tools/*.py` 文件在导入时调用 `registry.register()`。`model_tools.py:handle_function_call()` 按名称分发，独立调用可并行执行。

### MCP 集成（`tools/mcp_tool.py`）

Hermes 是一个完整的 MCP 客户端：

```yaml
# ~/.hermes/config.yaml
mcp_servers:
  loom:
    command: "docker"
    args: ["run", "--rm", "-i", "-v", "./skills:/loom/skills:ro",
           "timoxue/loom", "serve", "--port", "8080"]
    timeout: 120
```

启动时，Hermes 连接每个配置的 MCP 服务器，调用 `initialize` + `tools/list`，将发现的工具合并进注册表。LLM 选择 loom 工具时，Hermes 调用 `tools/call` 并将结果作为工具消息回传。

**这是今天已经可以工作的集成点。** Loom `--skills-dir` 中的任何 skill，都会成为 Hermes LLM 可用的工具。

---

## Loom 在哪里发挥作用

### 技术栈层次

```
┌──────────────────────────────────────────────────────┐
│  Hermes（Harness 层）                                  │
│  规划 · 记忆 · Skill 创建 · 工具分发                   │
├──────────────────────────────────────────────────────┤
│  Loom（执行审计闸门）          ← 你在这里              │
│  类型化 IR · 能力沙箱 · ShadowVFS · Receipt           │
├──────────────────────────────────────────────────────┤
│  真实文件系统 / 真实工作区                             │
└──────────────────────────────────────────────────────┘
```

Hermes 拥有"智能"层，Loom 拥有"执行安全"层。两者不互相替代。

### 各层职责划分

| 关注点 | 归属 |
|---|---|
| 做什么任务、如何规划 | Hermes |
| 调用哪个 skill | Hermes |
| 这次调用是否安全可执行？ | **Loom** |
| 这次调用实际改变了什么？ | **Loom** |
| 这些变更是否应该落到真实工作区？ | **Loom + 人工** |
| 是否需要合规留存记录？ | **Loom**（Receipt） |
| 从本次任务中学习以备下次使用 | Hermes（skill 补丁） |

---

## 当前缺口：Skill 创建绕过了 Loom

Hermes 的 `skill_manage(action="create")` 直接写入 `~/.hermes/skills/`——不经过 loom。唯一的门控是 `skills_guard.py` 的文本扫描。

**为什么这很重要：** Skill 就是代码。一个写着"把患者数据写入 `/tmp/export.csv` 然后 `curl` 出去"的 skill，文本扫描可能放行，但这正是 loom 能力模型要拦截的内容。今天，Hermes 的自进化循环可以产生无能力边界约束的 skill，等到执行时 loom 才能介入。

```
Hermes 创建 skill → skills_guard 扫描 → ~/.hermes/skills/  ← 没有 loom
后来：Hermes 调用 skill → loom MCP → 类型化 IR + 能力检查  ← 有 loom
```

缺口在"创建"和"执行"之间。Loom 在执行时捕获问题，但前提是 skill 已经被编译成 `.loom.json`。如果它还是 `SKILL.md` 上下文文档，loom 就看不到其中的具体操作。

---

## 集成路径

### 路径 1（今天，已可用）

Loom 作为 MCP 服务器运行，Hermes 通过 `mcp_servers` 配置连接。Loom `--skills-dir` 中的 skill 暴露为工具。经过 `loom migrate-openclaw` 迁移并由 `loom accept-migration` 签字的 skill，已完全受 loom 治理。

```
hermes claw migrate          # 导入 OpenClaw skills 到 ~/.hermes/skills/
loom migrate-openclaw        # 翻译为类型化 v1 .loom.json（未审核草稿）
loom accept-migration        # 人工签字
loom serve --skills-dir ./   # 暴露为 MCP 工具
hermes → loom MCP → 受治理的执行
```

### 路径 2（近期）

Hermes 创建新 skill → 不直接写入 `~/.hermes/skills/`，而是调用 `loom migrate-openclaw` 处理生成的内容 → Loom 产出未审核草稿 → 人工运行 `loom accept-migration` → Skill 可用。自进化能力保留，执行安全性叠加。

实现方式：将 `skill_manage` 的写目标配置为 loom 监听的目录，或实现一个薄适配层，将 `skill_manage` 输出包装成 loom MCP 调用。

### 路径 3（未来）

`skill_context` 步骤类型（见 `docs/loose-parser-design.md`）：纯 LLM 上下文文档类型的 Hermes skill，在 loom 中以 `skill_context` 步骤运行。每次 SOP 查阅都是一个可审计的 Receipt 事件——在合规监管环境中这是强制要求。

---

## `hermes claw migrate` 与 `loom migrate-openclaw` 对比

这两个命令看起来相似，但工作在不同层：

| 命令 | 做什么 | 输出 | 安全门控 |
|---|---|---|---|
| `hermes claw migrate` | 复制 `SKILL.md` 到 `~/.hermes/skills/openclaw-imports/` | `SKILL.md`（未修改）| `skills_guard.py` 文本扫描 |
| `loom migrate-openclaw` | 将 `SKILL.md` 翻译为类型化 v1 `.loom.json` | 未审核的 `.loom.json` | 解析 → 验证 → `accept-migration` 人工签字 |

两者互补。运行 `hermes claw migrate` 将 skill 导入 Hermes 的上下文系统；运行 `loom migrate-openclaw` 将同一批 skill 编译成 loom 可强制执行能力边界的 IR。

---

## 快速上手：Hermes → Loom

```bash
# 1. 启动 loom MCP sidecar
docker run -d -p 8080:8080 \
  -v ./skills:/loom/skills:ro \
  -v ./audit-log:/home/loom/.loom \
  timoxue/loom serve --skills-dir /loom/skills --port 8080

# 2. 配置 Hermes 连接 loom
cat >> ~/.hermes/config.yaml << 'EOF'
mcp_servers:
  loom:
    url: "http://localhost:8080/v1/mcp"
    timeout: 120
EOF

# 3. Hermes 现在可以看到 loom skill 作为工具
hermes "用 deseq2-workflow skill 处理 data.csv"
# → Hermes 选择 loom 工具，调用 tools/call
# → Loom：解析 → 验证 → ShadowVFS → Receipt
# → Hermes 在工具结果中看到 manifest
# → 用户运行：loom commit <session-id> --yes
```

---

## OpenClaw Medical Skills 与 `allowed-tools`

OpenClaw-Medical-Skills 中的 869 个 skill 有一个关键的 frontmatter 字段：`allowed-tools`。这是 Hermes 的能力声明——告诉 harness LLM 在使用该 skill 时被允许调用哪些工具。

```yaml
allowed-tools: Read, Edit, Write, Bash, WebFetch, WebSearch
```

Loom 的松散解析器将此字段直接映射为 loom 能力声明：

| `allowed-tools` 值 | Loom 能力 | 备注 |
|---|---|---|
| `Read`、`Glob`、`Grep` | `vfs.read: /` | 人工审核时收窄 scope |
| `Write`、`Edit` | `vfs.write: /` | 人工审核时收窄 scope |
| `Bash` | STUB: `os_command` | 能力缺口 — skill 变为 stub |
| `WebFetch`、`WebSearch` | STUB: `http_call` | 能力缺口 |
| `Task` | STUB: `agent_call` | loom 尚不支持 |

869 个 skill 中有 343 个包含 `allowed-tools` → **39% 可以机械化转换**为 loom 能力声明，无需 LLM 辅助。

---

## 一句话总结

> **Hermes 让 AI 知道做什么，Loom 确保 AI 只能做它说它要做的事，并且留下证明。**
