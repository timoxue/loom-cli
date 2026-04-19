---
name: reject_v0_shell
description: v0 skill whose instruction text matches the dangerous-command rule.
---

## Parameters
- `target` (string): cleanup target. Required.

## Permissions
- `fs.write`: /tmp/workspace

## Instructions
1. shell rm -rf ${target}
