---
sourceHash: sha256-20611daf
---

# SSH 通道

Agent 要修服务器，就得登录服务器。通常的做法是把私钥或密码交给它，从此它能从任何地方登录这把密钥能进的每一台机器，执行了什么也没有任何记录。Beacon 的 SSH 通道反过来做：Agent 用登记在自己令牌上的密钥登录 Beacon；Beacon 检查这个令牌能不能连这台机器，再用它为这一次连接签发的证书（五分钟有效）连到机器上，并把整个会话录下来。机器只信任 Beacon 的 CA（签发证书的那把密钥），所以 Agent 手里从来没有机器的密钥或密码；要收回它的权限，吊销它的令牌或者暂停通道就行。

这一页写给负责运行 Beacon、往里面添加机器的人。内容包括：打开通道、添加机器、给 Agent 权限、Agent 怎样用自己的 `ssh`、`scp`、`rsync` 或 Beacon 的 MCP 工具连上机器、记录了什么以及怎样回放、暂停、NAT 后面的机器，以及通道保证什么。

Beacon 不审核命令：执行什么由 Agent 自己判断，由运行它的程序把关。人看得到一切，也能暂停整个通道。

## 打开通道 {#turn-it-on}

参考部署已经替你做好了：`install.sh` 会生成两把密钥，`config.yaml` 里也写好了。在别的环境里，自己生成两把密钥，写进配置：

```bash
ssh-keygen -t ed25519 -N '' -C 'beacon host' -f secrets/ssh-host-key   # 通道自己的主机密钥
ssh-keygen -t ed25519 -N '' -C 'beacon ca' -f secrets/ssh-ca-key       # 用来签发证书
```

```yaml
ssh:
  listen: ":17002"
  public_address: ssh.example.com:17002   # Agent 连接的地址
  host_key_file: /etc/beacon/secrets/ssh-host-key
  ca_key_file: /etc/beacon/secrets/ssh-ca-key
  # source_addresses: [203.0.113.7/32]    # 设置后，证书只能从 Beacon 自己的地址使用
  keep: 4320h                             # 会话保留 180 天
```

通道是独立端口上的普通 SSH：直接开放这个端口（加一条防火墙规则），不要经过 HTTP 反向代理。`beacon check-config` 会检查两把密钥，两个配置项用了同一把密钥也会拒绝。之后 `beacon ssh status` 显示 `agents reach machines; the channel's host key is ssh-ed25519 AAAA…`。

拿到 CA 私钥的人，能登录每一台信任它的机器。请把它单独放在一个文件里，只让服务能读；Beacon 的地址固定的话，设置 `source_addresses`。

## 添加机器 {#add-a-machine}

在中心上执行：

```bash
docker exec beacon beacon machine add edge-1 edge-1.example.net -note "Los Angeles edge node"
```

Beacon 连接这台机器的 22 端口一次（也可以用 `HOST:PORT` 指定端口），固定它的主机密钥，然后打印机器上要做的配置：写进 `/etc/ssh/beacon_ca.pub` 的那一行，以及下面这些命令。这个名字在 Beacon 里处处代表这台机器，只能用小写字母、数字和连字符。在机器上以 root 执行：

```bash
echo 'ssh-ed25519 AAAA… beacon-ca' > /etc/ssh/beacon_ca.pub   # machine add 打印的那一行；beacon machine ca 可以再打印一次
echo 'TrustedUserCAKeys /etc/ssh/beacon_ca.pub' > /etc/ssh/sshd_config.d/beacon.conf
id beacon >/dev/null 2>&1 || useradd -m -s /bin/bash -p '*' beacon   # Beacon 证书里指定的账户
systemctl reload ssh
```

`-user` 可以指定 `beacon` 以外的账户；`-critical` 把机器标记为关键机器，提醒在上面干活的人（控制台和 `list_machines` 会显示）。然后：

- **账户能做什么，才是真正的边界。** 只给 `beacon` 干活需要的权限，比如在 `/etc/sudoers.d/beacon` 里写：

  ```
  beacon ALL=(root) NOPASSWD: /usr/bin/systemctl restart relay, /usr/bin/journalctl
  ```

  要不要放得更宽，由团队决定；无论怎样，Beacon 都会记下做了什么。
- **别让 OpenSSH 惩罚 Beacon 的地址。** OpenSSH 9.8 起，同一来源地址连接失败多了会被限制（`PerSourcePenalties`）。所有 Agent 都从 Beacon 的地址连过来，所以要在 `beacon.conf` 里加上 `PerSourcePenaltyExemptList 203.0.113.7`。
- **在机器上也留一份执行记录**（可选）：sshd 日志里记着证书的 key ID（「ID beacon 20261101-091724-aa8bba for claude-code via ssh」），能对上是哪次会话；再加一条 auditd 规则，比如 `-a always,exit -F arch=b64 -S execve -F auid>=1000 -k beacon`，会话启动的每个程序都会被记下来，脚本里调用的也一样。

`machine list` 列出所有机器；重装系统后，用 `machine add … -replace` 重新固定主机密钥；`machine remove NAME` 移除一台机器，它的会话记录仍然保留。Beacon 连不进去的机器要反过来添加：见 [NAT 后面的机器](#machines-behind-nat)。

## 给 Agent 权限 {#give-an-agent-access}

Agent 要连机器，令牌得有 `machines:ssh` 作用域。如果它要用自己的 ssh，令牌上还要带上它的公钥，也就是运行 Agent 的那台机器上的公钥：

```bash
docker exec beacon beacon token create claude-code \
  -scopes status:read,machines:ssh,sessions:read -expires 90d -ssh-key "ssh-ed25519 AAAA… agent@workstation"
```

- `-machines edge-1,edge-2` 限定令牌能连的机器；`token machines NAME all` 取消限定。
- 以后用 `token ssh-key NAME add KEY` 和 `token ssh-key NAME remove FINGERPRINT` 增删公钥。
- 同一个令牌同时用于 MCP 工具、REST API 和 SSH 通道。

## 用自己的 ssh、scp 和 rsync 连接 {#connect-with-your-own-ssh-scp-and-rsync}

`list_machines`（MCP）或 `GET /api/v1/machines` 返回机器列表、令牌能否连每台机器，以及一段 `ssh_config` 和一行 `known_hosts`。把这段配置放进 `~/.ssh/config`，把那一行放进 `~/.ssh/known_hosts`：

```
Host edge-1
  HostName ssh.example.com
  Port 17002
  User edge-1
  HostKeyAlias beacon
```

之后 `ssh edge-1 'systemctl status relay'`、`scp`、`sftp`、`rsync -e ssh` 和 `ssh -L` 都和平常一样用。用户名告诉 Beacon 要去哪台机器；会话在机器上以 `beacon` 账户运行。`HostKeyAlias beacon` 让 ssh 校验 Beacon 自己的主机密钥，所有机器都是同一把；每台机器固定下来的主机密钥，由 Beacon 自己校验。

通道拒绝连接时，Agent 的 ssh 会打印原因并以 1 退出，比如 `beacon: this token may not reach the machine edge-2; machines this token reaches: edge-1`。各种提示及处理方法见[常见问题](troubleshooting.md#the-agent-cannot-reach-a-machine)。

## MCP 工具 {#the-mcp-tools}

没有 shell 的 Agent，或者更想要结构化结果的 Agent，可以用同一个令牌调用 Beacon 的工具。这些工具的名字和用法都照着 Agent 自己的工具来：

| 工具 | 做什么 |
| --- | --- |
| `list_machines` | 机器列表、令牌能否连每台机器，以及给 Agent 自己的 ssh 用的 `ssh_config` 配置段和 `known_hosts` 行 |
| `run_command` | 执行一条命令（在 `sh` 里，以机器上的账户，不带终端）：返回退出码，以及纯文本的 stdout 和 stderr |
| `read_file` | 读文本文件的若干行，像 `cat -n` 一样带行号，可用 `offset` 和 `limit` |
| `write_file` | 写整个文件，最大 1 MiB，目录不存在时自动创建 |
| `edit_file` | 精确替换一段文字，替换一处或全部；文件在这期间被改过就拒绝 |
| `list_sessions`、`get_session` | 谁在哪里做了什么；某次会话的文字记录 |
| `add_machine`、`set_machine_connector`、`remove_machine`、`pause_ssh`、`resume_ssh` | 需要 `machines:write` |

每次工具调用都单独算一次会话，和其他会话一样录下来。REST API 在 `/api/v1/machines`、`/api/v1/sessions` 和 `/api/v1/ssh` 下提供同样的操作；每个参数见 [MCP 工具](reference/mcp.md)。

## 记录了什么 {#what-is-recorded}

每一次会话都有记录：一条命令、一个交互 shell、一次文件传输、一个转发端口，以及每一次工具调用。

- **录像**是 asciicast v3 格式，存在 `data_dir/recordings/YYYY/MM/DD/` 下。可以在控制台的会话页面（`/console/sessions/{id}`）回放，可以用 `asciinema play` 播放，也可以从 `GET /api/v1/sessions/{id}/recording` 下载。
- **文字记录**（`get_session`）里是命令的纯文本输出；交互 shell 则是每条命令及其输出和退出状态。Beacon 启动 bash 时带上 shell 集成标记，bash 会报出每一行命令，所以提前敲进去的命令也认得出来（这个 shell 会清空 `HISTCONTROL`，每条命令也都进了账户的历史记录）；其他 shell 照常录像，只是不拆分命令。
- **键入的内容**也会录下来。Agent 从不输入密码；人也不要在会话里输入密码。
- **告警群**里，每个 Agent 在每台机器上做的事，最多每分钟汇成一条消息：执行了什么、退出码是多少，每次会话附一个 **Replay** 链接。
- **告警**会列出相关机器上的会话：证据和控制台的告警页面都能看到告警前后谁执行了什么。
- 会话保留 `ssh.keep` 这么久（示例里是 180 天），之后连同文件一起删除。

## 暂停 {#pausing}

`beacon ssh pause`（或者 `pause_ssh`、`POST /api/v1/ssh/pause`，或者控制台「机器」页面上的开关）立刻关闭所有正在进行的会话，并拒绝新的会话，直到 `ssh resume`。`ssh status` 显示当前状态：`agents' access to machines is paused since …`。暂停期间，Agent 的 ssh 会收到 `beacon: agents' access to machines is paused since …; a person resumes it with beacon ssh resume`。

只想停下某一个 Agent 而不是全部，就吊销它的令牌：`beacon token revoke NAME`，或者在控制台的「令牌」页面操作（需要 `machines:write`）。从这一刻起，它的新连接和工具调用都会被拒绝；但它已经打开的连接会保持到自己结束，所以要立刻断开的话，同时暂停通道。

## NAT 后面的机器 {#machines-behind-nat}

有些机器 Beacon 连不进去：在 NAT 后面、在只许出不许进的防火墙后面、放在家里。这样的机器用它自带的 OpenSSH 客户端主动连到通道上，Beacon 再通过这条连接访问它的 sshd。机器上不用装任何东西。

**原理。** 机器用一把专用密钥运行 `ssh -N -R 0:localhost:22 edge-9@ssh.example.com -p 17002`，以自己的身份登录：用户名就是机器名，和 Agent 指定目标机器的方式一样。Beacon 根据匹配上的是哪把密钥，区分机器的连接和 Agent 的连接：机器的连接器密钥登记在机器的记录上，Agent 的密钥登记在它的令牌上。连接器的连接只能做一件事：申请这一个远程转发并保持心跳；不能开会话，不能开别的通道，不能发别的请求；每台机器只有一条连接，以最新的为准。Beacon 不为这个转发监听任何端口（它回报的端口号只是个标签）。Agent 要连这台机器时，Beacon 通过这条连接反向打开一个通道，连到机器自己转发的地址，在上面照常握手：校验固定的主机密钥，出示五分钟有效的证书。其余和普通机器完全一样：`ssh`、`scp`、`sftp`、`rsync`、`ssh -L`、MCP 工具、录像、暂停开关。

**添加。** 先在机器上以 root 生成它连出去用的密钥，再在 Beacon 上登记：

```bash
ssh-keygen -t ed25519 -N '' -C 'beacon connector edge-9' -f /etc/ssh/beacon_connect_key   # 在机器上
docker exec beacon beacon machine add edge-9 connector -key "ssh-ed25519 AAAA… beacon connector edge-9" -note "office NAS"   # 在中心上
```

`-key` 填机器上 `/etc/ssh/beacon_connect_key.pub` 的内容。`machine add` 会打印 `added edge-9 as a connector machine (user beacon): it connects out to Beacon with ssh -R; …`，以及一段给机器用的脚本：先和普通机器一样配置 CA 和 `beacon` 账户，再写入 Beacon 通道的 `known_hosts` 行（它在 `ssh.public_address` 上的主机密钥），创建拥有这把密钥的系统用户 `beacon-connector`，以及 systemd 单元 `beacon-connector.service`，带 `Restart=always`、`ServerAliveInterval=30` 和 `ExitOnForwardFailure=yes`。`add_machine`（`address: connector`、`connector_key`）和 `POST /api/v1/machines` 返回同样的内容，分别在 `connector.script`、`connector.command` 和 `connector.unit` 里。打印的命令转发到 22 端口；机器的 sshd 监听别的端口时，改一下即可。

- `machine connector NAME -key …`（`set_machine_connector`、`PUT /api/v1/machines/{name}/connector`）设置或更换密钥；用旧密钥建立的连接会被关闭，机器用新密钥重新连上来。添加机器时可以先不给 `-key`，以后再设。`machine add … -replace` 不带 `-key` 时保留原来的密钥。
- **机器的主机密钥**和其他机器一样要固定。可以用 `-host-key "ssh-ed25519 AAAA…"` 给出，内容是机器上 `/etc/ssh/ssh_host_ed25519_key.pub` 的那一行；也可以让 Beacon 在机器第一次连上来时自己学：Beacon 向转发后面的 sshd 要它的主机密钥，固定下来，在审计日志里写一条带指纹的 `machine_host_key`，并告诉告警群。自己学，意味着信任这第一次连接，好在这次连接用的是你装在机器上的密钥；一点都不想信任的话，就传 `-host-key`。重装之后，`machine add NAME connector -replace` 会重新学一次（也可以给 `-host-key`）。
- 在机器的 sshd 看来，每个会话都来自它自己（`127.0.0.1` 或 `::1`）：打印的配置里加了 `PerSourcePenaltyExemptList 127.0.0.1,::1`，Beacon 给这类机器签的证书，除了 `ssh.source_addresses` 也允许这两个来源。sshd 的日志照样用证书的 key ID 标出每次会话。

**Beacon 保证什么。**

- Beacon 只连机器自己转发的那个地址，从不连它自己挑的地址。
- 连接器密钥只能以它那台机器的身份登录，不能开会话，不能转发它自己的端口，也不持有令牌；令牌上的密钥申请不了转发。令牌持有的密钥不能用作连接器密钥。
- 暂停开关会关闭通过连接器中转的会话，和其他会话一样；机器自己的连接保持不断，所以通道一恢复，机器马上就能连。
- `machine list`、`list_machines` 和 `GET /api/v1/machines` 会显示 `connector: true`、`connected` 和 `connected_since`。连接器没有连上的机器会立刻失败：API 和 MCP 返回 `machine.disconnected`，Agent 的 ssh 收到 `beacon: the machine's connector is not connected`。连上、断开和替换都写日志，不回显到告警群；指标 `beacon_ssh_connectors` 是当前连着的连接器数，`beacon_ssh_connector_machines` 是以连接器方式添加的机器数，两者不相等时可以让规则告警。

**限制。**

- 每台机器一条连接，以最新的为准；Beacon 重启会断开所有连接，`Restart=always` 几秒内就把它们连回来，这期间机器处于 `disconnected`。
- 两跳：Agent 到 Beacon，Beacon 再通过机器建立的那条连接到机器，这台机器上的所有会话都走这一条连接。延迟是两跳之和，吞吐量就是这一条连接的吞吐量。
- Beacon 自己的心跳会关掉 90 秒没有应答的连接；掉线的机器过了这段时间显示为断开，网络恢复后自动重连。

## 通道保证什么，不保证什么 {#what-the-channel-guarantees-and-what-it-does-not}

它保证：

- **Agent 手里没有密钥。** Agent 用登记在令牌上的自己的密钥登录 Beacon；机器只信任 Beacon 的 CA；每张证书只用于一次连接，写明机器上的账户，五分钟有效。设置了 `source_addresses` 的话，证书只能从 Beacon 的地址使用。
- **只能连令牌允许的机器**，而且只在通道没有暂停的时候。没有 `machines:ssh` 的令牌，或者只限其他机器的令牌，在 Beacon 连接任何机器之前就被拒绝。
- **连上的就是你添加的那台机器。** 每次连接，Beacon 都校验机器固定下来的主机密钥，不一致就拒绝。
- **一切都有记录**：每次会话和工具调用都录下来，列在这台机器相关的告警下面，并回显到告警群。

它不做：

- **不审核命令，也不会自己执行任何东西。** 每次会话都由 Agent 发起；执行什么由 Agent 自己决定，能做到什么程度，取决于它在机器上那个账户的权限。
- **证书到期时不会断开正在进行的会话。** 证书只在 Beacon 连接机器的那一刻校验；已经打开的会话会持续到它自己结束、通道被暂停，或者 Beacon 重启。
- **不传递客户端的环境变量**（使用机器自己的语言环境和设置），不给 Agent 做远程端口转发（`ssh -R` 是 NAT 后面的机器连出来的方式，Agent 不能用），不支持 Agent 转发和 X11。
