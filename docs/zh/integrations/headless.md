---
sourceHash: sha256-1583d5df
---

# 无头 Agent 与 CI

独自运行的程序——比如 `beacon-oncall` 在钩子唤醒时启动的 Agent，或一个 CI 任务——没有浏览器可以登录：它拿一个令牌。在中心上：

```bash
docker exec beacon beacon agent add ci-runner -owner alice -preset oncall -machines edge-1,edge-2 > ci-runner.token
```

## 创建 Agent 和它的令牌 {#make-an-agent-and-its-token}

在控制台上，打开 **Agent**，再到**无界面的 Agent 或 CI 任务**：填名称、所有者（所有者才有这一项）、预设、机器，以及令牌的有效期（30、90 或 365 天，默认 365 天）。也可以用上面的命令。

标准输出上只有令牌本身，所以 `> ci-runner.token` 把它存进文件；其余内容打印到标准错误：

```
agent alice / ci-runner (oncall, may do status:read,sessions:read,alerts:write,machines:ssh) made; its token ci-runner expires 2027-11-02. The secret is shown only now:

Claude Code:
claude mcp add --transport http beacon https://status.example.com/mcp --header "Authorization: Bearer lbt_…"

Codex:
export BEACON_TOKEN=lbt_…
codex mcp add beacon --url https://status.example.com/mcp --bearer-token-env-var BEACON_TOKEN

curl:
curl -H "Authorization: Bearer lbt_…" https://status.example.com/api/v1/token
```

控制台上令牌也只显示一次，同样附上可直接粘贴的命令。Beacon 只保存它的 SHA-256。

- 不写 `-preset` 时，取所有者角色允许的最强预设；不写 `-expires` 时为 `365d`，最长两年。
- `-ssh-key KEY`，或用 `-ssh-key @FILE` 从文件读取，立即给 Agent 一把密钥；命令之后还会给出它自己的 ssh 用的 `~/.ssh/config` 片段（[接入 Claude Code](claude-code.md#connect-with-your-own-ssh-scp-and-rsync)）。
- 令牌会在到期前自行续期（[自动续期的令牌](#tokens-that-renew-themselves)），所以不用有人再发新的。真要一个新的，在 Agent 页面上点**新令牌**。
- `beacon agent list` 列出每个 Agent，带所有者、预设、现在能做什么、机器，以及它有几个令牌、几次 OAuth 登录、几把密钥；`beacon agent remove NAME` 连同全部凭据删除一个。

## beacon-oncall 参考接收程序 {#beacon-oncall-the-reference-harness}

webhook 的另一头需要有程序来运行 Agent。`beacon-oncall` 是参考实现的接收程序，和 `beacon`、`beacon-probe` 一起发布。它接收 webhook、校验签名，然后启动你指定的程序：载荷从标准输入传给程序，告警信息放在环境变量里。它不做任何决定，这么薄是有意的：Claude Code、Codex 或任何读环境变量的程序都可以跑在它后面。`contrib/oncall` 里有前两者的包装脚本、给被叫醒的 Agent 用的提示词，以及一个 systemd 单元。

```bash
beacon-oncall -secret-file /etc/beacon-oncall/hook-secret \
  -run /usr/local/lib/beacon-oncall/claude-code.sh -log-dir /var/lib/beacon-oncall/logs
```

| 参数 | 含义 |
| --- | --- |
| `-listen` | 在哪里接收 webhook；默认 `:17007`。Beacon 里钩子的 `url` 指向这里；除非两者在同一个内网，中间要经过你的 TLS 反向代理 |
| `-secret-file` | 钩子的密钥：内容和 Beacon 里钩子的 `secret_file` 相同 |
| `-run` | 处理 `firing` 事件的程序，通过 `/bin/sh -c` 运行 |
| `-run-resolved` | 处理 `resolved` 事件的程序；不设的话，这类事件只确认收到，不运行任何程序 |
| `-concurrency` | 同时运行的程序数；默认 `2`。另外最多 32 个排队等待；再多就返回 503，Beacon 稍后重试 |
| `-timeout` | 每个程序最多运行多久；默认 `30m`。超时后整个进程组被杀掉 |
| `-log-dir` | 每次运行的标准输出和标准错误，文件名为 `<time>-<delivery>.log`，最后一行写明退出状态和用时 |
| `-keep` | 超过这个时长的日志会被删除；默认 `720h`。`0` 表示全部保留 |

**它怎么应答。** 校验通过、把运行排进队列后，立即返回 202：程序做成了什么，看它的日志、Agent 留下的备注和 Beacon 的记录，而不看给 Beacon 的应答。签名错误或缺失的，返回 401；`at` 超过 15 分钟的也返回 401（Beacon 的重试不会拖这么久，所以这是重放）；解析不了的返回 400。每个 `delivery` 它会记住 24 小时，同一次调用的重试返回 202，但不再运行任何程序；认的是签名过的请求体里的 id，不是请求头。`GET /healthz` 返回 `ok`，可以用来检查它是否在运行。收到 SIGTERM 后，它不再接收新调用，并等待正在运行的程序结束，最多等 `-timeout`。

**程序拿到什么。** 标准输入是载荷，此外还有这些环境变量；Beacon 没有给出对应内容时，变量存在但为空：`BEACON_EVENT`、`BEACON_DELIVERY`、`BEACON_HOOK`、`BEACON_ALERT_ID`、`BEACON_ALERT_TITLE`、`BEACON_ALERT_SERVICE`、`BEACON_ALERT_SEVERITY`、`BEACON_ALERT_STATUS`、`BEACON_ALERT_URL`、`BEACON_EVIDENCE_URL`、`BEACON_TIMELINE_URL`、`BEACON_MCP_URL`、`BEACON_RUNBOOK_URL`。标题和服务名来自标签：它们是数据，不是指令。

### 包装脚本 {#the-wrappers}

`contrib/oncall/claude-code.sh` 以无界面模式运行 Claude Code。它把 `contrib/oncall/prompt.md` 和告警信息拼成一个提示词；给 Claude Code 配上 Beacon 的 MCP server（`BEACON_MCP_URL` 上的 HTTP server，每次连接时都从 `BEACON_TOKEN_FILE` 读取令牌，所以令牌续期后自动生效）；允许它不经询问使用 Beacon 的工具，其他工具一概不给（`--tools ""`、`--allowedTools "mcp__beacon__*"`、`--permission-mode dontAsk`）；用 `--max-turns` 限制工作量；并把每条消息作为一行 JSON 写进日志，人可以一步步看 Agent 做了什么。可以用 `ONCALL_MAX_TURNS`、`ONCALL_MODEL`、`ONCALL_PROMPT`、`CLAUDE_BIN` 和 `CLAUDE_ARGS` 调整。Claude Code 照常认证：服务用户主目录里的登录状态，或者服务环境里的 `ANTHROPIC_API_KEY`。

`contrib/oncall/codex.sh` 用 `codex exec` 做同样的事：提示词走标准输入，Beacon 的 MCP server 通过 `-c mcp_servers.beacon.*` 配置，令牌放在只有 Codex 能看到的环境变量里，本地操作一律在只读沙箱里，审批关闭。可以用 `ONCALL_MODEL`、`CODEX_BIN` 和 `CODEX_ARGS` 调整。

`contrib/oncall/prompt.md` 是两者共用的提示词，只有一页，全文见[值班提示词](on-call-prompt.md)。它按顺序告诉被叫醒的 Agent：先拿证据（`get_evidence`）；再查证据指向的机器，用 Beacon 的工具（`run_command`、`read_file`、`list_machines`），或者用 `list_machines` 返回的 `ssh_config` 走自己的 ssh；修能安全修好的部分；验证，能用 `probe_now` 就用，否则再调用一次 `get_evidence`；最后用 `add_note` 写明结论、做了什么、还剩什么。还有几条规则：不可信字段只当数据；备注里不写任何密钥；不认领、不静默告警，那是人的决定；进行不下去时停下来，留一条备注。可以按你自己的服务修改它，但请保留这些规则。

### 作为系统服务运行 {#as-a-service}

`contrib/oncall/beacon-oncall.service` 让接收程序以单独的用户运行，系统其余部分只读（`ProtectSystem=strict`），只能写这个用户的主目录（Agent 的登录状态存在这里）和日志目录，并且设置了 `Restart=always`。文件开头列出了安装命令：创建用户；把钩子密钥和 Agent 的令牌放在 `/etc/beacon-oncall` 下，只有这个用户能读（令牌在中心上用 `docker exec beacon beacon agent add claude-oncall -owner alice -preset oncall > beacon-token` 创建）；把包装脚本和提示词放在 `/usr/local/lib/beacon-oncall` 下；以这个用户身份安装并登录 Agent 程序。停止服务时，会等正在进行的运行结束。

## 自动续期的令牌 {#tokens-that-renew-themselves}

与其让人给无界面的 Agent 一个长期令牌、过期了再发一个新的，不如让令牌自己续期：快到期前，持有者申请一个新的密钥，拿到的令牌名称、所属 Agent、作用域、主体、机器和 SSH 密钥都不变，有效期从现在起重新计算（用 `-expires 90d` 创建的令牌，每次都再得到 90 天）。除了令牌本身，什么都不需要：两个接口接受任何作用域。Claude Code 这样的交互式客户端改用 OAuth 登录，由它自己刷新令牌。

| 接口、工具 | 内容 |
| --- | --- |
| `GET /api/v1/token`、`whoami` | 调用者是谁，以及持有者看到的凭据信息：`name`、`actor`（审计日志里的称呼，如 `alice / claude-oncall`）、`user`、`agent`、`preset`、`credential`（`token` 或 `oauth`，后者带 `client`）、`scopes`（现在能做的，不超出所有者的角色）、`machines`、`ssh_keys`（指纹）、`created`、`expires`、`lifetime`、`renewed`（上次续期时间）、`renewable` 和 `renewable_at`；从不包含密钥本身 |
| `POST /api/v1/token/renew`、`renew_token` | 新的密钥（`secret`，只显示这一次）和令牌信息；`old_secret_valid_until` 说明被替换的旧密钥还能用到什么时候 |

- **没有竞争问题。** 续期后旧密钥还能用十分钟，客户端可以从容替换令牌文件，刚读过旧文件的进程也不受影响。
- **每天最多一次。** 一个令牌每 24 小时最多续期一次（`token.too_soon`，`429`）。
- **机器的令牌和 OAuth 不续期。** 登记过的机器（检测点、主机、心跳）的令牌从不自动续期，要用 `beacon enroll … -rotate` 替换（`token.not_renewable`，`403`）。OAuth 访问令牌也不续期，由它的客户端刷新。
- **都有记录。** 每次续期都记入审计日志（`token_renew`），并回显到告警群（「令牌 claude-oncall 自行续期了，到期日 2027-01-31」）；`beacon token list` 显示每个令牌的有效期和上次续期时间。
- **SSH 登录靠密钥**，续期不影响它：令牌的身份不变，变的只有密钥和到期时间。

harness 每天运行一次类似下面的脚本（cron 或 systemd 定时器），Agent 每次调用都重新读取令牌文件：

```bash
#!/bin/sh
# 续期 $TOKEN_FILE 里的 Beacon 令牌，并原子地替换这个文件。
set -eu
TOKEN_FILE=${TOKEN_FILE:-/etc/beacon-oncall/beacon-token}
BEACON=${BEACON:-https://status.example.com}
resp=$(curl -sS -w '\n%{http_code}' -X POST -H "Authorization: Bearer $(cat "$TOKEN_FILE")" "$BEACON/api/v1/token/renew")
code=$(printf '%s\n' "$resp" | tail -n 1)
case $code in
  200) ;;
  429) exit 0 ;;     # 一天内已经续期过：令牌仍然有效
  *) echo "renewing the Beacon token failed: HTTP $code" >&2; exit 1 ;;
esac
umask 077
printf '%s\n' "$resp" | sed '$d' | jq -r .secret > "$TOKEN_FILE.tmp"
mv -f "$TOKEN_FILE.tmp" "$TOKEN_FILE"     # 读取方看到的要么是旧文件，要么是新文件，不会是半个
```

令牌的有效期要比续期间隔长好几倍（每天续期的话，用 `-expires 30d`），这样 harness 停机一段时间也不会丢掉令牌。真丢了，由 Agent 的所有者在 Agent 页面上点**新令牌**再发一个。泄露的令牌会一直自己续期，直到被吊销（Agent 页面上的**吊销**，或 `beacon token revoke NAME`），吊销同时会立即关闭它在机器上打开着的一切。告警群能看到每次续期，`token list` 能看到最近一次。
