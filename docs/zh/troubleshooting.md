---
sourceHash: sha256-8e277aaa
---

# 常见问题

这一页按现象来排：哪里不对劲、为什么、该怎么办。Beacon 的提示会说明出了什么问题，通常也会说明怎么解决；下面引用的就是这些提示的原文，也就是命令行、日志、API 和 Agent 的 ssh 打印出来的样子（它们都是英文）。命令以参考部署为准，在中心的 `/opt/beacon` 下执行；其他部署方式去掉 `docker exec beacon`，直接运行 `beacon`。

大多数问题，看这三个地方就能找到答案：

- `docker compose logs beacon`（服务日志，每行一个 JSON）、`docker compose logs vmalert`，以及检测点机器上的 `journalctl -u beacon-probe -n 50`。
- `GET /api/v1/bridge`，或者 MCP 工具 `status_summary`：告警现在能不能送到人，不能的话为什么。在中心上，`curl http://127.0.0.1:17001/readyz` 不用令牌也能看到同样的原因（参考部署没有通过 Caddy 开放 `/readyz`）。
- VictoriaMetrics 的界面，中心上的 `http://127.0.0.1:8428/vmui`：检测点实际发来了什么。

## 安装 {#installing}

### install.sh 中途停下 {#installsh-stops-with-a-message}

`install.sh` 在启动任何东西之前先检查需要的条件，遇到第一个问题就停下，输出 `install.sh: …` 和原因：`run it as root`、`Docker is not installed`、`Docker Compose v2 is missing`、`ssh-keygen is missing`、`Let's Encrypt refuses example.com addresses; use a real one`、`the two host names must differ: Caddy routes by name`。文件没有通过校验时，它会写明是哪个文件，并给出查看原因的命令，比如 `the rules do not validate: docker compose run --rm --no-deps vmalert -dryRun -rule='/etc/vmalert/rules/*.yml'`。按提示改好，再运行一次；这个脚本重复运行多少次都没关系。

`not every container is healthy after 3 minutes` 说明四个容器里有一个没起来：`docker compose ps` 看是哪个，`docker compose logs <name>` 看原因。`the rule engine's alerts have not reached the gateway in 2 minutes` 指向 vmalert：看 `docker compose logs vmalert`。

### 第一张证书没有签下来 {#the-first-certificate-is-not-issued}

`curl https://status.example.com/healthz` 报 TLS 错误。启动后第一张证书最多要等一分钟。过了一分钟还不行，就检查两个域名是否都解析到中心（`dig +short status.example.com`）、80 和 443 端口是否能从公网访问，再看 Let's Encrypt 回复了什么：`docker compose logs caddy`。

### beacon 命令连不上正在运行的服务 {#a-beacon-command-cannot-reach-the-running-service}

`beacon token list` 之类的命令失败，提示 `cannot reach the running service at /var/lib/beacon/ctl.sock: …` 和 `(run this inside the container: docker exec beacon beacon …)`。命令行通过数据目录里的管理套接字和服务通信，这个套接字只在服务运行的地方才有。参考部署里服务在容器内，所以要执行 `docker exec beacon beacon token list`。其他部署方式下，用服务自己的用户或 root 运行（套接字拒绝其他用户）；数据目录不是 `/var/lib/beacon` 时，设置 `BEACON_DATA_DIR`。

### 改了 config.yaml 却没有生效 {#a-change-to-configyaml-does-not-take-effect}

Beacon 只在启动时读一次 `config.yaml`。改完之后（告警群、钩子、SSH 通道等），先校验再重启：`sudo ./install.sh` 会修正新密钥文件的属主和权限并校验所有文件，然后执行 `docker compose restart beacon`。`SIGHUP` 不会重读 `config.yaml`，它只导入目录、检测目标和模块文件（见[下文](#an-import-or-sighup-did-not-change-the-configuration)）。

## 检测点 {#probes}

### 检测点没有数据 {#a-probe-location-shows-no-data}

vmui 里这个检测点没有 `probe_success` 序列，控制台总览里它算作「应上报」却没有上报，或者 `ProbeLocationDown` 触发了（检测点 10 分钟没有发来任何数据）。在检测点机器上运行 `journalctl -u beacon-probe -n 50`，日志会说明原因：

| 日志里看到 | 原因 | 怎么办 |
| --- | --- | --- |
| `401`、`auth.invalid` | 令牌文件不是 `beacon enroll probe` 打印的那个，或者已经被 `-rotate` 换掉 | 用 `-rotate` 重新登记，再用新令牌运行 `probe/install.sh -token-file FILE` |
| `403`、`location.inactive`、`location edge-2 is not in the catalogue` | 目录里没有这个检测点的 id，或者导入的文件里没有它，它被删掉了 | 把它加进目录再导入，或者通过 API 添加（见[检测什么，从哪里检测](catalog.md)） |
| `403`、`the machine of location edge-2 has expired` | 有一个和检测点同名、类型为 `vps` 的资源，日期已经过了 | 给这个资源续一个新日期 |
| `cannot fetch from the gateway; using the cached copy` 加连接错误 | 这台机器连不上 `metrics.example.com` 的 443 端口（或网关自己的端口） | 检查 DNS、防火墙，以及 `/etc/beacon-probe/agent.env` 里的地址 |

登记时也会检查目录：目录里还没有 `edge-2` 时，`beacon enroll probe edge-2` 会报 `location edge-2 is not in the catalogue`；第二次登记会报 `probe-edge-2 is enrolled already; enroll it with -rotate to replace its token`。

检测点在上报，但你的某个目标不在其中：由谁检测，取决于目标集的名字。`all-…` 是所有检测点，`overseas-…` 和 `mainland-…` 是对应地区的检测点，`<location id>-…` 只有那一个检测点（见[检测目标](catalog.md#targets)）。改动在一分钟内生效。

如果数据来得晚或者一阵一阵地来，检查检测点机器的时钟：检测结果带时间戳，时钟没有和 NTP 同步时，`probe/install.sh` 会给出警告。

### 某个目标一直失败 {#a-target-always-fails}

浏览器里能打开的目标，`probe_success` 却一直是 0。原因在 `probe_failure_info{reason}` 和证据里：`dns`、`connect`、`timeout`、`tls`、`not_ssl`、`http_status`、`body`、`banner`、`protocol`、`auth`、`manifest` 或 `config`。检测程序每次失败也会记一条 `probe failed` 日志，带上模块、目标、原因和细节。

想亲眼看看，就在检测点机器上用它拿到的模块文件手动检测一次：

```bash
sudo -u beacon-probe /opt/beacon-probe/bin/beacon-probe run -modules /var/lib/beacon-probe/state/modules.yml http_head https://example.com/
```

它打印检测结果的指标；失败时还会打印 `failed: <reason>: <detail>`。常见原因：模块要求 200，服务却返回重定向（`http_status`；看 `follow_redirects` 和 `valid_status_codes`）；服务不接受 `HEAD` 请求；模块用了 `password_file`，检测程序的密钥目录里却没有这个文件（`config`）；只有从这个检测点出发的线路慢（`timeout`；看检测点的 `probe.timeout` 和 `probe.retries`）。

## 告警 {#alerts}

### 一直没有告警 {#an-alert-never-arrives}

你以为该有告警，`GET /api/v1/alerts`（或 `list_alerts`）里却没有。顺着告警的路径查：

1. **失败的检测点够不够？** 至少两个检测点失败，并且失败的至少占有数据的检测点的一半，才算服务故障；只有一个检测点失败，什么都不会触发，这是有意设计的。在 vmui 里查 `probe_success{service="…"}`，能看到每个检测点的结果。
2. **失败的时间够不够长？** 检测点连续失败 3 分钟才算失败，`ServiceDown` 再过 1 分钟才触发：也就是服务出问题后 4 到 5 分钟。`ServiceDegraded`（至少两个但不到一半的检测点失败）和 `MainlandPathDegraded` 要等 5 分钟。
3. **规则引擎连得上 Beacon 吗？** 收不到 vmalert 的告警时，`GET /api/v1/bridge` 会显示 `no Watchdog from the rule engine since start` 或 `the Watchdog from the rule engine stopped`。然后看 `docker compose logs vmalert`：它的 notifier 必须指向网关（`-notifier.url=http://beacon:17006`），并带上规则令牌（`rules.token_file`）或者有 `alerts:send` 的令牌。
4. **是不是被压下去了？** 匹配这条告警的静默会让它不发通知：看 `beacon silence list`。维护窗口也会创建这样的静默，备注以 `maintenance:` 开头。抑制规则会在另一条告警触发时压住这一条；参考部署里，同一服务的 `ServiceDown` 会压住 `ServiceDegraded` 和 `MainlandPathDegraded`。

演练期间，`curl -i https://status.example.com/drill/healthz` 应该返回 `503` 和 `drill`；`beacon drill status` 显示演练到什么时候结束。

### 有告警，但没人收到通知 {#nobody-is-told-of-an-alert}

`list_alerts` 里有这条告警，但没有卡片、没有消息、手机也没响。

- **没有配置通知渠道。** 没有 `feishu:` 段，也没有 `pushover:` 时，告警只记录、只显示，日志里写着 `alert recorded; no notification channel is configured`。按[配置飞书应用](feishu.md)配好，再重启 Beacon。
- **通知渠道出错了。** 日志里有 `feishu card failed; falling back` 或 `alert could not be delivered by any channel`，`GET /api/v1/bridge` 会列出 `the Feishu long connection is down`、`a Feishu API call failed in the last 10 minutes` 或 `the Feishu API budget for this month is used up`。
- **发给了自定义机器人。** `info` 级别的告警发给群里的自定义机器人，是纯文本消息，不是卡片；应用用掉 `feishu.monthly_budget` 的 70% 之后，警告级别的告警也是这样。
- **还没到打电话的时候。** 严重告警没人认领时，10 分钟后在飞书应用里加急值班的人，15 分钟后发 Pushover。认领之后，两者都停 `ack_ttl`（默认 4 小时）。

### Agent 没有被叫醒 {#the-agent-is-not-woken}

告警触发了，钩子对应的 Agent 却什么也没做。先看这个告警的调用记录：`get_alert` 里的 `agents` 列出每次调用的状态、HTTP 状态码、尝试次数和错误，控制台的 **Hook** 页面也列出所有调用。

- **一次调用都没有。** 钩子只响应它列出的级别（默认 `[critical]`）；设了 `service` 的话，正则表达式必须匹配整个 `service` 标签。钩子只在新告警组第一次发出通知时调用，所以你加钩子之前就已经在触发的告警组，不会叫醒它。钩子写在 `config.yaml` 里，新增或修改钩子都要重启。
- **`beacon-oncall` 返回 401。** 它的日志里有 `refused`，`reason` 是 `bad signature`（接收程序的 `-secret-file` 和钩子的 `secret_file` 不一致）、`no signature`，或者 `stale delivery`（载荷里的 `at` 比接收程序的时钟早了 15 分钟以上：检查两边的时钟）。
- **404 或连接错误。** 钩子的 `url` 到不了接收程序：检查它前面的反向代理，以及 `-listen`（默认 `:17007`）。在接收程序所在的机器上，它运行时 `curl http://127.0.0.1:17007/healthz` 会返回 `ok`。
- **返回了 202，还是没有动静。** 接收程序已经收下调用；之后发生了什么，看 `-log-dir` 下这次运行的日志，每次调用一个文件，最后一行是程序的退出状态。程序启动不了，或者 Agent 没有以服务用户的身份登录，都会写在那里。

## Agent 与机器 {#agents-and-machines}

### Agent 连不上机器 {#the-agent-cannot-reach-a-machine}

Agent 的 ssh 失败，或者 `run_command` 之类的工具返回错误。通道会说明原因：ssh 上是一行以 `beacon:` 开头的提示，退出状态为 1；MCP 和 API 上是一个错误码。

| Agent 看到 | 原因 | 怎么办 |
| --- | --- | --- |
| `Permission denied (publickey)` | ssh 出示的密钥不属于任何此刻能用通道的 Agent 或令牌：从没登记过、Agent 没有还能用的凭据、它的预设不给 `machines:ssh`，或者它的所有者已停用 | 让 Agent 用 `add_ssh_key` 登记，或者在 Agent 页面上加上（**添加 SSH 密钥**）；再在 Agent 页面上确认它有能用的凭据，以及它的预设 |
| `beacon: this token may not reach the machine: the token lacks the machines:ssh scope`（`machine.forbidden`） | Agent 的预设或所有者的角色不给 `machines:ssh` | 在 Agent 页面上把预设改为**值班**或**运维**（不能超出所有者的角色）；独立令牌则新建一个带这个作用域的 |
| `beacon: no such machine called "edge-3"; machines this token reaches: edge-1, edge-2`（`machine.not_found`） | 用户名不是任何一台机器的名字 | 看 `beacon machine list`；ssh 的用户名就是机器名 |
| `beacon: this token may not reach the machine edge-2; machines this token reaches: edge-1`（`machine.forbidden`） | Agent，或者它所用的 OAuth 登录，只限其他机器 | 在 Agent 页面上加上这台机器；OAuth 登录则从客户端重新登录并勾选它；独立令牌用 `beacon token machines NAME edge-1,edge-2`，或者 `all` |
| `beacon: agents' access to machines is paused since …`（`ssh.paused`） | 有人暂停了通道 | 和暂停它的人确认后，执行 `beacon ssh resume` |
| `beacon: the machine cannot be reached: edge-1: dial tcp …`（`machine.unreachable`） | Beacon 连不上机器的 SSH 端口 | 机器宕了，或者它的防火墙不让中心连进来 |
| `… ssh: handshake failed: ssh: unable to authenticate …`（`machine.unreachable`） | 机器的 sshd 不接受 Beacon 的证书 | 执行 `machine add` 打印的那几行：`/etc/ssh/beacon_ca.pub`、`TrustedUserCAKeys`、`beacon` 账户、`systemctl reload ssh` |
| `… ssh: handshake failed: ssh: host key mismatch`（`machine.unreachable`） | 机器的主机密钥和 Beacon 固定的不一样，比如重装过系统 | 先弄清楚为什么变了，再执行 `beacon machine add NAME HOST -replace` |
| `beacon: the machine's connector is not connected; …`（`machine.disconnected`） | NAT 后面的机器没有连到通道上 | 在机器上看 `systemctl status beacon-connector`；`beacon machine list` 会显示 `disconnected` |
| `beacon: the SSH channel is not configured`（`ssh.disabled`） | 没有设置 `ssh.ca_key_file` | 见[打开通道](ssh.md#turn-it-on) |

还有两种情况。如果连续失败几次之后，所有 Agent 连同一台机器都开始失败，可能是 OpenSSH 的惩罚机制（9.8 及以后的版本）限制了中心的地址，而所有 Agent 都从这个地址连过来：在机器的 `beacon.conf` 里加上带中心地址的 `PerSourcePenaltyExemptList`（见[添加机器](ssh.md#add-a-machine)）。另外，同一个地址 10 分钟内登录失败 20 次后，通道在这 10 分钟剩下的时间里，对这个地址的连接一律不应答、直接关闭。

### Agent 的凭据被拒绝 {#the-agents-credential-is-refused}

API 或 MCP 返回 401 或 403，或者 MCP 客户端说连不上，或者要求重新登录。

- **`auth.invalid`，`the token is unknown or has expired`。** 令牌过期了、被吊销了，或者输错了；也可能是 Agent 被删除了，或者它的所有者被停用了。`beacon token list` 能看到每个令牌的到期时间和上次续期时间，Agent 页面上能看到它的各个凭据。在 Agent 页面上点**新令牌**新建一个（独立令牌用 `beacon token create`），并让 Agent 能自己续期（见[自动续期的令牌](agents.md#tokens-that-renew-themselves)）。
- **OAuth 客户端要求重新登录。** 它的登录被吊销了，或者授权页面上允许的期限到了：从客户端重新登录（Claude Code 里用 `/mcp`，Codex 用 `codex mcp login beacon`）。
- **令牌续期了，客户端手里还是旧的。** 像 `claude mcp add --header` 这样把请求头写死的客户端，会一直用旧密钥，而旧密钥在续期十分钟后失效：用新密钥重新添加一次。`contrib/oncall` 里的包装脚本则是每次连接都重新读取令牌文件。
- **`auth.scope`。** 接口或工具需要凭据没有的作用域。提示里写明了是哪个：独立令牌是 `the token lacks the … scope`，Agent 则还会说明它的预设在所有者角色范围内允许什么。在 Agent 页面上修改预设，或者请所有者给你一个允许这样做的角色（见[作用域](reference/api.md#scopes)）。
- **`auth.too_many_failures`。** 同一个地址 10 分钟内失败 20 次，这 10 分钟剩下的时间里都会被拦住；常见于客户端拿着错误的令牌不停重试。
- **续期时返回 `token.too_soon`（429）。** 令牌不到 24 小时前刚续过期，仍然有效。

### probe_now 报告有检测点缺席 {#probe_now-names-locations-as-missing}

`probe_now` 返回的 `missing` 里有检测点，或者 `complete` 为 false。检测点在 `wait` 时间内没有回答，就算缺席：它的检测程序挂了（见[检测点没有数据](#a-probe-location-shows-no-data)），或者它的 `beacon-probe` 版本太旧，还不支持按需重测。`probe.busy`（429）表示这个目标不到 15 秒前刚按需重测过，或者这个令牌已经有三个请求在进行；等一会儿再试。`probe.not_found`（404）表示没有在用的检测点检测这个目标：检查目标有没有拼错，以及它所在目标集的名字。

## 控制台与状态页 {#the-console-and-status-pages}

### 登录控制台失败 {#signing-in-to-the-console-fails}

`/console/sign-in` 的登录表单会说明原因（括号里是界面语言为中文时的提示）：

- `Wrong username or password.`（「用户名或密码不对。」）：两者有一个不对，提示对两种情况都一样。忘了密码，请所有者给一个重设链接（**成员**页面，或 `beacon user reset NAME`）。
- `This account is disabled; an owner can enable it.`（「这个账号已停用；所有者可以重新启用。」）：所有者在**成员**页面上启用它，或者执行 `beacon user enable NAME`。
- `Too many failed sign-ins; try again in a few minutes.`（「登录失败的次数太多，请过几分钟再试。」）：这个用户名 10 分钟内密码错了 10 次，不论来自哪里；等这 10 分钟过去。
- `Too many failed sign-ins from here; try again in a few minutes.`（「这里登录失败的次数太多，请过几分钟再试。」）：你的地址 10 分钟内失败了 20 次，等这 10 分钟过去。
- 一次又一次地提示 `The form has expired; try again.`（「表单已过期，请重试。」）：浏览器没有保存登录表单的 cookie。控制台的 cookie 带 `Secure`，必须通过 HTTPS（或 `localhost`）打开；用普通 HTTP 打开，这个表单永远不会成功。请通过 `public_url` 上的 TLS 代理访问。
- 改用令牌登录时：`The token is unknown or has expired.`（「令牌不存在或已过期。」，令牌输错了、过期了或者被吊销了），或者 `Token NAME lacks status:read, which the console needs to show anything.`（「令牌 NAME 没有 status:read，控制台需要它才能显示内容。」，换一个带 `status:read` 的令牌）。

还没有人有账号？这时登录页面只提供令牌登录，并会说明：初始化链接在 Beacon 的日志里，`beacon setup` 能再打印一个（见[第一位所有者](accounts.md#the-first-owner)）。初始化、邀请或重设链接提示 `This link does not work: it was used already, it expired, or it is wrong.`（「这个链接不能用：已用过、已过期或不正确。」）时，请所有者重新给一个。详见[控制台](pages.md#signing-in)。

### 打开旧链接，控制台显示 404 {#the-console-says-404-on-an-old-link}

告警群、卡片或 Agent 备注里的链接，打开后显示找不到页面。签名链接无论因为什么失败，都是同样的 404，这样错误的链接不会泄露任何信息。常见原因：

- **链接过期了。** 链接的有效期是 `pages.link_ttl`，默认 30 天。登录后在 `/console/alerts` 里找到这个告警，或者在 `/console/sessions` 里找到这次会话。
- **告警已经不在了。** 告警组恢复 7 天后，Beacon 就不再保留它，它的页面也就没有了，登录了也一样。它的会话保留 `ssh.keep` 这么久，备注都在审计日志里，状态页上的事件保留 91 天。
- **链接被截断了。** 签名是链接末尾的 `?s=…` 部分；复制时漏了这部分，或者链接被缩短了，都会失败。
- **在状态页自己的域名上打开。** 那个域名上只有状态页；控制台在 `public_url` 上。
- **数据库换过了。** 链接用 Beacon 存在数据库里的一把密钥签名；换了新数据库，就是一把新密钥，之前的链接都会失败。

0.3.0 之前的链接指向站点根路径下的 `/alerts/{id}` 或 `/sessions/{id}`，在过期之前仍然能用：它们会被转到（`308`）`/console` 下的同一个页面（见[签名链接](pages.md#signed-links)）。

### 状态页显示无数据 {#a-status-page-shows-no-data}

某个组件或整个页面显示 **No data yet**（中文界面为「暂无数据」），历史条上是灰色的日子。状态页显示的是检测点每分钟的一致判断，所以无数据就是这个组件的服务没有任何检测结果：

- **组件写了一个没有任何检测目标带着的服务名。** `beacon check-config` 和添加这个页面的那次修改都会给出警告：`status page "public": component "website" names service "example.com", which no probe target has; it shows no data until one does`。服务名必须和检测目标的 `service` 标签完全一致。
- **组件写的 `page` 标签只出现在内部检测目标上。** 标为 `visibility: internal` 的检测目标，永远不会通过 `page` 支撑一个组件；警告是 `… which no public probe target carries …`。
- **没有检测点在上报。** 检测程序挂了，或者连不上网关：见[检测点没有数据](#a-probe-location-shows-no-data)。
- **VictoriaMetrics 没有应答。** 日志里会有 `status pages: reading the probes failed`。

检测点开始上报之前的日子会一直是灰色；过去的某一天，在它结束后的六小时内还会重新计算，好把线路中断期间检测点暂存的结果算进去。规则详见[状态页](status.md)。

## 配置 {#configuration}

### 导入或 SIGHUP 之后配置没变 {#an-import-or-sighup-did-not-change-the-configuration}

你改了目录或检测目标的文件，改动却没有生效。

- **文件不会被自动读取。** 首次启动之后，配置存在 Beacon 的数据库里。要导入文件：先 `docker exec beacon beacon config import /etc/beacon -dry-run`，再去掉 `-dry-run` 执行一次。
- **SIGHUP 被拒绝了。** 只有在上次导入之后没人通过 Beacon 改过配置时，`SIGHUP` 才会导入文件，这样文件就不会悄悄覆盖人和 Agent 做的修改。被拒绝时日志里写着 `SIGHUP: the configuration files were not imported; keeping the configuration in force`，以及 `… changed through Beacon since the last import (version 5: put by claude-code via mcp); keep it with beacon config export DIR, or replace it with beacon config import DIR`。二者选一个做。
- **文件没有通过校验。** 这时什么都不会改，提示会说明原因；`beacon check-config` 会同时检查文件和数据库。

`nothing to change: version N holds this already` 表示文件和数据库已经一致。

### 修改被拒绝：config.conflict 或 config.invalid {#a-change-is-refused-with-configconflict-or-configinvalid}

`config.conflict`（409）：这次修改基于一个旧版本（`if_version`），而现在生效的是更新的版本；或者要撤销的修改所涉及的条目，后来又被别的修改动过。重新读一遍（`GET /api/v1/config`、`get_config`），再重新决定。`config.invalid`（422）：这次修改通不过校验，提示会写明哪一条，配置没有任何改变。常见的一种：只要还有目标集以某个检测点命名，这个检测点就不能删除。规则详见[检测什么，从哪里检测](catalog.md)。
