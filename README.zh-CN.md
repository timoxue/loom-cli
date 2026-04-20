# loom

English | [中文](README.zh-CN.md)

> AI agent 的执行审计闸门 — 先拦，后录。

Loom 专注于 agent 技术栈的一个特定层：将不可信的执行意图转化为能力受限、沙箱隔离、可审计的文件系统变更，并拒绝在没有人类明确签字的情况下将其推送到真实工作区。它**不是** agent 框架，**不是**记忆系统，**不是**提示词构建层——它是这些系统可以接入的执行底座。

**状态**：可用原型，day 7。v1 类型化 IR 已稳定。MCP sidecar 和 OpenClaw 迁移路径可用，但边缘情况仍有粗糙之处。

---

## 为什么做这个

AI agent 现在有了"手"。Cursor 帮你写代码，Claude Code 编辑你的配置，Devin 给你开 PR。两种极端同样危险：

- **"完全信任 agent"** — 一次提示词注入或一个幻觉出来的 `rm -rf`，你的工作区就没了。
- **"把 agent 关进 Docker"** — 沙箱太粗，没有准入概念，没有审计链路，没有"这步可以但那步不行"的粒度。

Loom 用五条由代码而非策略强制执行的原则填补这个空白：

1. **类型化歧义即安全歧义。** IR 不含 `any` 或 `map[string]interface{}`；每个步骤都是带封闭枚举 kind 的类型标记联合体。
2. **能力携带作用域。** `vfs.write` 单独是无界的；`vfs.write` + `out/` 是验证器对每个步骤参数强制检查的可验证声明。
3. **执行前先准入。** 在任何步骤分发之前，写入带规范逻辑哈希的收据。
4. **变更前先隔离。** 所有写操作经由 `ShadowVFS` 落入可丢弃的临时目录。真实工作区只读，直到人类明确推送。
5. **稳定核心，可替换边缘。** IR 是稳定契约；解析器、agent、协议位于边缘。

完整架构：[docs/architecture.md](docs/architecture.md)（英文）/ [docs/architecture.zh-CN.md](docs/architecture.zh-CN.md)（中文）。

---

## 快速开始

```bash
# 构建
go build -o loom ./cmd

# 在沙箱工作区执行 v1 skill
./loom run test_skills/templated_write.loom.json --input msg=世界

# 查看 manifest，然后推送
./loom commit <session-id> --yes

# 或：启动 MCP sidecar，让 agent 接入
./loom serve --skills-dir test_skills --port 8080
```

运行 `./loom --help` 查看完整命令列表。子命令帮助在 `./loom <cmd> --help`。

---

## Loom 与知识市场的关系

经常被问到的一个问题是：loom 是否应该成为一个 skill 市场——一个 agent 发布验证过的 skill、相互发现解决方案的地方。

**答案是否定的，原因在于定位。** 市场和运行时处于技术栈的不同层：

| 层 | 关注点 | 示例 |
|---|---|---|
| 知识市场 | "这个问题该用哪个 skill？" | EvoMap 的 Capsule + 声誉 + 赏金经济 |
| Agent Harness | "如何规划、记忆、调用工具、验证？" | Claude Code、OpenAI Agents SDK、LangGraph |
| **Tools + Guardrails 运行时** | **"这个工具调用安全吗？能审计吗？"** | **loom** |
| 操作系统 | "这个进程能写这个文件吗？" | Linux 内核、seccomp |

Loom 只占其中一行。

### 组合方式

```
┌─ 知识市场（EvoMap、内部注册表、skill git 仓库...）
│     ↓  以 .loom.json 形式获取已验证的 skill
├─ Agent Harness（Claude Code、OpenAI Agents、你自己的...）
│     ↓  通过 MCP 调用工具，或直接 loom run
├─ loom   ← 你在这里
│     ↓  Receipt、manifest、审计链路
└─ 市场层，用于发布执行证明 / EvolutionEvent
```

---

## 目录结构

- [internal/engine/](internal/engine/) — IR、验证器、清洗器、执行器、ShadowVFS、编译器、提交闸
- [internal/engine/parser/](internal/engine/parser/) — v0 OpenClaw markdown 解析器 + v1 JSON 解析器
- [internal/migrator/](internal/migrator/) — OpenClaw → v1 迁移工具，带来源签名审核
- [cmd/](cmd/) — CLI 子命令（`verify`、`run`、`commit`、`serve`、`migrate-openclaw`、`accept-migration`）
- [test_skills/](test_skills/) — 参考 v1 skill + 锚定在信任边界的负向测试夹具
- [toys/](toys/) — 100 行 Python 探针，在真实 Claude 会话中通过 MCP 驱动 loom
- [docs/](docs/) — 架构文档（中英双语）、迁移指南、开发日志

## 测试

```bash
go test -count=1 ./...
```

[cmd/fixtures_test.go](cmd/fixtures_test.go) 中的夹具矩阵将每个负向用例锚定到必须被捕获的具体层——解析、验证、清洗或执行。即使后续层仍能保护工作区，让路径逃逸在验证器之后才被清洗器捕获也会导致测试失败。这个纪律正是重点所在。

## 贡献

如果你打算修改 IR 或执行器，请先阅读 [CLAUDE.md](CLAUDE.md)——它记录了不可商量的约定（例如 `any` 在核心中被禁止，commit 必须人工触发，Description 和 Provenance 被排除在逻辑哈希之外）。

## 许可证

Apache 2.0。详见 [LICENSE](LICENSE)。
