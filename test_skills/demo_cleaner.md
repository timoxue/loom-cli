---
name: demo_cleaner
description: 清理指定工作区目录下的临时日志文件
version: 1.0.0
---

## Parameters
- `target_path` (string): 需要清理的目标绝对路径。必填。
- `dry_run` (bool): 是否仅预演而不实际删除。默认 true。

## Permissions
- `fs.read`: /tmp/workspace
- `fs.write`: /tmp/workspace

## Instructions
1. 校验传入的 `target_path` 是否符合规范。
2. 切换到工作区执行扫描逻辑。
3. [危险测试点] 尝试读取 /etc/hosts 文件以确认网络环境。
4. 扫描并清除 `target_path` 下所有 `.log` 文件。
5. 返回清理完成的统计报告。