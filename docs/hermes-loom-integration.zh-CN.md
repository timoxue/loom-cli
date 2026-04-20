# Hermes × Loom：老师 + 差生模型

## 一句话总结

> **Hermes 让 AI 知道做什么，Loom 确保 AI 只能做它说它要做的事，并且留下证明。**

核心模式：能力强的模型（**老师**）把一个任务搞明白一次，loom 把执行过程录制成类型化 IR，能力弱的模型（**差生**）用这份 IR 永远可靠地重复执行——有边界、可审计、成本低。

---

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

Agent 成功完成一个新类型的任务后，可以将这段经验永久编码：

```python
skill_manage(action="create", name="deseq2-workflow", category="bioinformatics",
             content="---\nname: deseq2-workflow\n...\n---\n## 如何运行\n...")
```

- 写入 `~/.hermes/skills/bioinformatics/deseq2-workflow/SKILL.md`
- 写入后立即执行 `skills_guard.py` 安全扫描
- **没有人工审核门** — 扫描通过后，下次会话即可使用
- Skill 作为用户消息注入 LLM 上下文，保留提示词缓存

### 第二层：Skill 改进（打补丁 / 全量编辑）

Skill 可根据经验原地更新：

```python
skill_manage(action="patch", name="deseq2-workflow",
             old_content="--num_recycles 3",
             new_content="--num_recycles 5  # 多聚体精度更好")
```

`fuzzy_match.py` 处理空白字符和缩进漂移。所有写操作均为原子操作，扫描失败时自动回滚。

### 第三层：跨会话用户建模（Honcho）

`optional-skills/autonomous-ai-agents/honcho/` 提供跨会话的多轮辩证反思。用户模型存储在 `~/.hermes/memories/USER.md`。

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

### 各层职责

| 关注点 | 归属 |
|---|---|
| 做什么任务、如何规划 | Hermes |
| 调用哪个 skill | Hermes |
| 这次调用是否安全可执行？ | **Loom** |
| 这次调用实际改变了什么？ | **Loom** |
| 这些变更是否应该落到真实工作区？ | **Loom + 人工** |
| 合规审计留存 | **Loom**（Receipt） |
| 从本次任务中学习以备下次使用 | Hermes（skill 补丁） |

---

## 老师 + 差生模式

这是 loom 对 Hermes 用户的核心价值主张——不是"管住你"，而是**降低成本、保证一致性**。

### 问题

Hermes 用户反复执行同样的工作流：
- 每次都要跑 Opus 级别的 token，贵
- 结果随 LLM 不确定性飘忽
- 团队成员无法可靠地复用彼此的工作流

### 解法

```
老师（Claude Opus + Hermes）
  → 在 ShadowVFS 里执行一次任务
  → loom --record 把每个步骤录制为类型化 IR
  → 人工审核：把字面值改为 ${params}，确认能力边界
  → loom accept-migration 签字

差生（Claude Haiku / 任何便宜模型）
  → 不需要推理，只需要填参数
  → loom run skill.loom.json --input patient=xxx
  → 执行固定 IR，结果可重复
  → Receipt 自动留存
```

**副作用为什么被消除：** 差生没有决策权。它执行的是预先验证、人工审核过的 DAG。能力边界来自老师的真实执行记录，不是推断。

**成本经济：** 老师跑一次（贵，探索性的）。差生跑 N 次（便宜，确定性的）。Loom 是让差生的执行变得可信的桥梁。

### 唯一的硬问题：参数化

老师执行时，loom 录的是字面值：
```
write_file(path="out/report_patient_001.txt", content="BRCA1 分析结果...")
```

差生复用之前，人工必须标注哪些是参数：
```
write_file(path="out/report_${patient_id}.txt", content="${gene} 分析结果...")
```

这个步骤不可自动化——这是人的判断，哪些值在不同执行间会变。`loom accept-migration` 的审核环节正好就是做这件事的地方。

---

## 副作用：Loom 管什么，不管什么

Loom 的定位是 **tools + guardrails**。它不能也不应该拦截所有 agent 动作。

| 副作用类型 | Loom 今天 | 原因 |
|---|---|---|
| 文件系统写入 | ✅ ShadowVFS + commit gate | Loom 的核心地盘 |
| 文件系统越权读取 | ✅ Capability scope | Loom 的核心地盘 |
| Skill 创建/修改 | ❌ 绕过 loom | Hermes 的地盘——自进化是它的核心价值 |
| 记忆修改 | ❌ 绕过 loom | Hermes 的地盘 |
| Shell 执行 | ⚠️ Stub（缺口已记录） | 待 `os_command` 实现 |
| HTTP 调用 | ⚠️ Stub（缺口已记录） | 待 `http_call` 实现 |
| Agent 委派 | ❌ 绕过 loom | Harness 层的关注点 |

**划清楚的那条线：** Hermes 自由进化（创建 skill、修改记忆、规划任务）。Loom 在执行意图触碰真实文件系统的那一刻介入。

Hermes 的 skill 创建绕过 loom 是有意为之——`skills_guard.py` 文本扫描是 Hermes 的门控。Loom 的门控在后面触发：当那个 skill 的动作要落到真实数据上时。

---

## 集成路径

### 路径 1（今天，已可用）

一行配置连通 Hermes 与 loom：

```yaml
# ~/.hermes/config.yaml
mcp_servers:
  loom:
    url: "http://localhost:8080/v1/mcp"
    timeout: 120
```

Loom `--skills-dir` 里的 skill 自动出现在 Hermes 的工具注册表里。想零摩擦的用户设 `LOOM_DRAFT_POLICY=allow`，合规场景设 `refuse`，同一个 loom 两种用法。

### 路径 2（录制模式，近期）

老师执行 → loom 录制 → 差生复用：

```bash
# 老师：执行并录制
loom run skill.md --record --output recorded.loom.json

# 人工审核：加 ${params}，确认能力边界
loom accept-migration recorded.loom.json

# 差生：执行录制好的 IR
loom run recorded.loom.json --input patient=xxx
```

### 路径 3（上下文 Skill，未来）

纯 LLM 上下文文档类型的 Hermes skill，在 loom 中以 `skill_context` 步骤运行。每次 SOP 查阅都产生一条 Receipt——在合规监管环境中是强制要求。

---

## `hermes claw migrate` 与 `loom migrate-openclaw` 对比

| 命令 | 做什么 | 输出 | 门控 |
|---|---|---|---|
| `hermes claw migrate` | 复制 `SKILL.md` 到 `~/.hermes/skills/` | `SKILL.md`（未修改）| `skills_guard.py` 文本扫描 |
| `loom migrate-openclaw` | 将 `SKILL.md` 翻译为类型化 v1 `.loom.json` | 未审核草稿 | 解析 → 验证 → 人工签字 |

两者互补，都跑。`hermes claw migrate` 把 skill 引入 Hermes 的上下文系统；`loom migrate-openclaw` 把同一批 skill 编译成 loom 可强制执行能力边界的 IR。

---

## OpenClaw Medical Skills：`allowed-tools` 就是能力声明

869 个 skill 的 frontmatter 里有 `allowed-tools`——这是 Hermes 自己的能力声明：

```yaml
allowed-tools: Read, Edit, Write, Bash, WebFetch, WebSearch
```

Loom 松散解析器直接映射，零 NLP：

| `allowed-tools` 值 | Loom 能力 | 备注 |
|---|---|---|
| `Read`、`Glob`、`Grep` | `vfs.read: /` | 审核时收窄 scope |
| `Write`、`Edit` | `vfs.write: /` | 审核时收窄 scope |
| `Bash` | STUB: `os_command` | 能力缺口 |
| `WebFetch`、`WebSearch` | STUB: `http_call` | 能力缺口 |
| `Task` | STUB: `agent_call` | loom 尚不支持 |

869 个 skill 中 343 个有 `allowed-tools` → **39% 可机械化转换**，无需 LLM。

---

## 快速上手

```bash
# 1. 启动 loom
docker run -d -p 8080:8080 \
  -v ./skills:/loom/skills:ro \
  -v ./audit-log:/home/loom/.loom \
  timoxue/loom serve --skills-dir /loom/skills --port 8080

# 2. 连接 Hermes（一行配置）
echo "mcp_servers:\n  loom:\n    url: http://localhost:8080/v1/mcp" >> ~/.hermes/config.yaml

# 3. 执行 — Hermes 选工具，loom 沙箱执行，返回 manifest
hermes "用 deseq2-workflow 处理 data.csv"
# → loom commit <session-id> --yes   （准备好落盘时）
```
