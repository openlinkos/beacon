---
sourceHash: sha256-33c21440
---

# GitHub Actions

`kind: github` 的钩子唤醒一个 GitHub workflow。Beacon 触发它时带两个输入：`alert`（告警组 id）和 `event`（`firing` 或 `resolved`）。workflow 必须声明它们：

```yaml
on:
  workflow_dispatch:
    inputs:
      alert: {description: Beacon alert group id, required: true}
      event: {description: firing or resolved, required: false, default: firing}

jobs:
  oncall:
    if: inputs.event == 'firing'
    runs-on: ubuntu-latest
    steps:
      - name: Evidence
        env:
          ALERT: ${{ inputs.alert }}
          BEACON_TOKEN: ${{ secrets.BEACON_TOKEN }}
        run: |
          curl -fsS -H "Authorization: Bearer $BEACON_TOKEN" \
            "https://status.example.com/api/v1/alerts/$ALERT/evidence" > evidence.json
      # 接下来是 Agent 自己的步骤，带上 Beacon 的 MCP server 和它自己的令牌
```

像上面这样通过 `env` 传入输入值，不要直接写进脚本。能触发这个 workflow 的人都能决定输入的内容。运行 Claude Code 的完整 workflow 见下文[在 workflow 里运行 Claude Code](#run-claude-code-in-the-workflow)。

## Beacon 里的钩子 {#the-hook-in-beacon}

把钩子写进 Beacon 的配置：

```yaml
agents:
  - name: ops-workflow
    kind: github
    repository: example/ops
    workflow: oncall.yml
    ref: main
    token_file: /etc/beacon/secrets/github-dispatch-token
```

| 配置项 | 含义 |
| --- | --- |
| `repository` | `owner/name` |
| `workflow` | workflow 的文件名或 id |
| `ref` | 分支；默认 `main` |
| `token_file` | 指向一个能触发这个 workflow 的令牌：对这个仓库有 Actions 读写权限的细粒度令牌 |

钩子什么时候被调用、怎样重试、每次调用的结果记在哪里，见[让 Agent 值班](../guide/on-call.md#wake-an-agent)。

## 在 workflow 里运行 Claude Code {#run-claude-code-in-the-workflow}

对 `kind: github` 的钩子，`contrib/github/oncall.yml` 是现成的 workflow，接收 `alert` 和 `event` 两个输入。它用同一份 `prompt.md`（复制到 `.github/oncall/prompt.md`）拼出提示词。然后通过 Claude Code 的 GitHub Action 运行 Claude Code，并接上 Beacon 的 MCP server：

- 令牌取自仓库 secret `BEACON_TOKEN`；
- 地址取自变量 `BEACON_MCP_URL`；
- 同样只允许使用 Beacon 的工具。

这个 secret 是专为这个 workflow 创建的 Agent 的令牌：`beacon agent add ops-workflow -owner alice -preset oncall`（[无头 Agent 与 CI](headless.md#make-an-agent-and-its-token)）。其余信息都由 Agent 用自己的令牌向 Beacon 查询。输入值通过环境变量传递，从不写进脚本。
