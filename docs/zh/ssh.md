---
sourceHash: sha256-39257971
---

# SSH 通道

Agent 经 Beacon 到达机器：用它自己的 `ssh`、`scp`、`sftp`、`rsync` 和 `ssh -L`，或者用 MCP 工具（`run_command`、`read_file`、`write_file`、`edit_file`）。Agent 从来拿不到机器的密码或密钥。Beacon 为每次连接签一张 5 分钟有效的证书，机器只信任 Beacon 的 CA。每次会话都录制下来可以回放，交互 shell 拆成一条条命令并带退出码，并回显到告警群。

Beacon 不审核命令：执行什么由 Agent 自己判断，由它自己的工具链来做。人看得到一切，也能暂停整个通道。

## 打开 {#turn-it-on}

生成两把密钥，写进配置：

```bash
ssh-keygen -t ed25519 -N '' -C 'beacon host' -f secrets/ssh-host-key   # 通道自己的主机密钥
ssh-keygen -t ed25519 -N '' -C 'beacon ca' -f secrets/ssh-ca-key       # 用来签证书
```

```yaml
ssh:
  listen: ":17002"
  public_address: ssh.example.com:17002   # Agent 连接的地址
  host_key_file: /etc/beacon/secrets/ssh-host-key
  ca_key_file: /etc/beacon/secrets/ssh-ca-key
  # source_addresses: [203.0.113.7/32]    # 证书只能从 Beacon 自己的地址使用
  keep: 4320h                             # 会话保留 180 天
```

通道是独立端口上的普通 SSH：直接开放这个端口（防火墙规则），不要经过 HTTP 反向代理。`beacon check-config` 会检查两把密钥。

CA 私钥能登录每一台信任它的机器。单独放一个文件，只有服务能读；Beacon 的地址固定时，配上 `source_addresses`。

## 添加机器 {#add-a-machine}

```bash
docker exec beacon beacon machine add edge-1 edge-1.example.net -note "洛杉矶边缘节点"
```

Beacon 连接一次，固定机器的主机密钥，并打印机器需要做的配置。在机器上以 root 执行：

```bash
docker exec beacon beacon machine ca > /etc/ssh/beacon_ca.pub   # 或者粘贴它打印的那一行
echo 'TrustedUserCAKeys /etc/ssh/beacon_ca.pub' > /etc/ssh/sshd_config.d/beacon.conf
useradd -m -s /bin/bash -p '*' beacon   # Beacon 证书里写的账号
systemctl reload ssh
```

- **账号能做什么，才是真正的边界。**只给 `beacon` 干活需要的权限，比如 `/etc/sudoers.d/beacon`：

  ```
  beacon ALL=(root) NOPASSWD: /usr/bin/systemctl restart relay, /usr/bin/journalctl
  ```

  要不要放得更宽由团队决定；无论怎样，Beacon 都会记下做了什么。
- **让 OpenSSH 不惩罚 Beacon。**OpenSSH 9.8 起，连接失败后会按来源地址惩罚（`PerSourcePenalties`）。所有 Agent 都从 Beacon 的地址连过来，所以要在 `beacon.conf` 里加上 `PerSourcePenaltyExemptList 203.0.113.7`。
- **机器本身也留一份执行记录**（可选）：sshd 日志里的证书 ID（「ID beacon 20261101-091724-aa8bba for claude-code via ssh」）写明了是哪次会话；再加一条 auditd 规则，比如 `-a always,exit -F arch=b64 -S execve -F auid>=1000 -k beacon`，它启动的每个程序都会记下来，脚本里的也一样。
- 重装后用 `machine add … -replace` 重新固定主机密钥；`machine remove NAME` 移除机器（它的会话记录保留）；`machine list` 列出所有机器。

Beacon 连不进去的机器反过来添加：见 [NAT 后面的机器](#machines-behind-nat)。

## NAT 后面的机器 {#machines-behind-nat}

Beacon 够不到的机器（在 NAT 后面、只许出不许进的防火墙后面、家里的一台盒子）自己连出来到通道上，用它已经有的 OpenSSH 客户端，Beacon 再经这条连接到达它的 sshd。机器上不用装任何东西。

**原理。**机器运行 `ssh -N -R 0:localhost:22 edge-9@ssh.example.com -p 17002`，用一把专用密钥，以自己的身份登录：用户名就是机器名，和 Agent 指定目标的方式一样。Beacon 靠匹配到的密钥分辨机器的连接和 Agent 的连接：机器的连接器密钥登记在机器的记录上，Agent 的密钥登记在它的令牌上。连接器连接只能做一件事：申请这一个远程转发并保持心跳；不能开会话，不能开其他通道，不能发其他请求；每台机器一条连接，最新的那条生效。Beacon 不为这个转发监听任何端口（它回报的端口号只是一个标签）。Agent 指向这台机器时，Beacon 经这条连接反向打开一个通道，到机器自己转发的那个地址，并在上面照常握手：固定的主机密钥、5 分钟有效的证书。其他一切和普通机器一样：`ssh`、`scp`、`sftp`、`rsync`、`ssh -L`、MCP 工具、录制、暂停开关。

**添加。**在机器上以 root 生成它连出用的密钥，然后在 Beacon 上登记：

```bash
ssh-keygen -t ed25519 -N '' -C 'beacon connector edge-9' -f /etc/ssh/beacon_connect_key   # 在机器上
docker exec beacon beacon machine add edge-9 connector -key "$(cat /etc/ssh/beacon_connect_key.pub)" -note "办公室 NAS"
```

`machine add` 会打印一段给机器用的脚本：和普通机器一样的 CA 与 `beacon` 账号，再加上 Beacon 通道的 `known_hosts` 行（它在 `ssh.public_address` 上的主机密钥）、一个拥有这把密钥的系统用户 `beacon-connector`，以及 systemd 单元 `beacon-connector.service`，带 `Restart=always`、`ServerAliveInterval=30` 和 `ExitOnForwardFailure=yes`。`add_machine`（`address: connector`、`connector_key`）和 `POST /api/v1/machines` 返回同样的内容：`connector.script`、`connector.command`、`connector.unit`。打印的命令转发到 22 端口；机器的 sshd 监听别的端口时改一下即可。

- `machine connector NAME -key …`（`set_machine_connector`、`PUT /api/v1/machines/{name}/connector`）设置或更换密钥；用旧密钥建立的连接会被关闭，机器用新密钥重连。机器可以先不带 `-key` 添加，之后再给。`machine add … -replace` 不带 `-key` 时保留原密钥。
- **机器的主机密钥**和其他机器一样要固定。用 `-host-key "$(cat /etc/ssh/ssh_host_ed25519_key.pub)"` 给出，或者让 Beacon 在机器第一次连上来时学习：Beacon 向转发后面的 sshd 要它的密钥，固定下来，在审计日志里写一条带指纹的 `machine_host_key`，并告诉告警群。学习意味着信任这第一次连接，而它是用你装在机器上的密钥登录的；什么都不想信任，就传 `-host-key`。重装之后，`machine add NAME connector -replace` 会重新学习（或者接受 `-host-key`）。
- 机器的 sshd 看到每个会话都来自它自己（`127.0.0.1` 或 `::1`）：打印的配置里加了 `PerSourcePenaltyExemptList 127.0.0.1,::1`，Beacon 给这类机器签的证书在 `ssh.source_addresses` 之外也允许这两个来源。sshd 的日志照样用证书的 key ID 标出每次会话。

**Beacon 保证什么。**

- Beacon 只连到机器自己转发的那个地址，从不连它自己选的地址。
- 连接器密钥只能以它自己那台机器的身份登录，不能开会话，不能转发自己的端口，也不能持有令牌；令牌的密钥登记不了转发。令牌持有的密钥不能成为连接器密钥。
- 暂停开关关闭经连接器中转的会话，和其他会话一样；机器自己的连接保留，所以通道一恢复机器就能用。
- `machine list`、`list_machines` 和 `GET /api/v1/machines` 显示 `connector: true`、`connected` 和 `connected_since`。连接器没连上的机器会立刻失败：API 和 MCP 返回 `machine.disconnected`，Agent 的 ssh 看到 `beacon: the machine's connector is not connected`。连上、断开和替换都写日志，不回显到群里；gauge `beacon_ssh_connectors` 是当前连着的连接器数，`beacon_ssh_connector_machines` 是添加为连接器的机器数，两者不等时可以触发规则。

**限制。**

- 每台机器一条连接。最新的生效；Beacon 重启会断掉全部连接，`Restart=always` 几秒内把它们带回来，期间机器是 `disconnected`。
- 两跳：Agent 到 Beacon，Beacon 经机器自己建立的那条连接到机器，这台机器的所有会话都走这一条。延迟是两跳之和；吞吐量就是这一条连接的吞吐量。
- Beacon 自己的心跳会关闭 90 秒不应答的连接；掉线的机器在此之后显示为断开，恢复后自动重连。

## 给 Agent 权限 {#give-an-agent-access}

一个预设能到达机器的 Agent（值班或运维），加上它的公钥（[成员、Agent 与登录](accounts.md)）。Claude Code 这类交互式客户端用 OAuth 登录，自己用 `add_ssh_key` 登记公钥；无界面的 Agent 在控制台的 **Agent** 页面创建，或在主机上：

```bash
docker exec beacon beacon agent add claude-code -owner alice -preset oncall \
  -expires 90d -ssh-key "$(cat ~/.ssh/id_ed25519.pub)"
```

`-machines edge-1,edge-2` 限定它能到达的机器；以后在 Agent 页面上修改它的机器和公钥，Agent 自己的 `add_ssh_key` 和 `remove_ssh_key` 也可以。不属于 Agent 的独立令牌仍在主机上设置：`beacon token create NAME -scopes … -ssh-key KEY -machines a,b`，之后用 `token ssh-key NAME add KEY` 和 `remove FINGERPRINT`、`token machines NAME all`。吊销令牌、删除 Agent、降低或停用它的所有者、移除公钥，或把机器从它的列表里去掉，都会立即关闭它在不再能到达的机器上打开着的东西。

Agent 的凭据同样用于 MCP，工具有：

| 工具 | 做什么 |
| --- | --- |
| `list_machines` | 机器列表、令牌能否到达每台，以及给 Agent 自己的 ssh 用的 `ssh_config` 配置段和 `known_hosts` 行 |
| `run_command` | 执行一条命令（在 `sh` 里、以机器上的账号、不带终端）：返回退出码，以及纯文本的 stdout 和 stderr |
| `read_file` | 读文本文件的若干行，像 `cat -n` 那样带行号，可用 `offset` 和 `limit` |
| `write_file` | 写整个文件，最大 1 MiB，目录不存在时自动创建 |
| `edit_file` | 精确替换一段文字，一处或全部；文件在这期间被改过就拒绝 |
| `list_sessions`、`get_session` | 谁在哪里做了什么；某次会话的记录 |
| `add_machine`、`set_machine_connector`、`remove_machine`、`pause_ssh`、`resume_ssh` | 需要 `machines:write` |

REST API 在 `/api/v1/machines`、`/api/v1/sessions` 和 `/api/v1/ssh` 下提供同样的操作。

要用 Agent 自己的 ssh，把 `list_machines` 返回的 `ssh_config` 放进 `~/.ssh/config`，把 `known_hosts` 那一行放进 `~/.ssh/known_hosts`：

```
Host edge-1
  HostName ssh.example.com
  Port 17002
  User edge-1
  HostKeyAlias beacon
```

之后 `ssh edge-1 'systemctl status relay'`、`scp`、`rsync -e ssh` 和 `ssh -L` 都照常使用。用户名告诉 Beacon 要去哪台机器；在机器上，会话以 `beacon` 账号运行。

## 记录什么 {#what-is-recorded}

每一次会话：一条命令、一个交互 shell、一次文件传输、一个转发端口，以及每一次工具调用。

- 录制格式是 asciicast v3，存在 `data_dir/recordings/YYYY/MM/DD/` 下，可以用 `asciinema play` 播放，或在网页里用 asciinema-player 播放。`GET /api/v1/sessions/{id}/recording` 可以下载。
- 会话记录（`get_session`）里有命令的纯文本输出；交互 shell 则是每条命令及其输出和退出码。Beacon 启动 bash 时带上 shell 集成标记，由 bash 报出每条命令行，所以提前输入的命令也认得出来（这个 shell 会清空 `HISTCONTROL`，每条命令也都会进账号的历史记录）；其他 shell 照常录制，只是不拆分命令。
- 输入的内容也会录下来。Agent 从不输入密码；人也不要在会话里输入密码。
- 告警群里，每个 Agent 在每台机器上做的事最多每分钟汇成一条消息：执行了什么、退出码是多少。
- 会话保留 `ssh.keep` 这么久，之后连同文件一起删除。

## 暂停 {#pausing}

`beacon ssh pause`（或者 `pause_ssh`，或者 `POST /api/v1/ssh/pause`）关闭所有正在进行的会话，并拒绝新的会话，直到 `ssh resume`。`ssh status` 显示当前状态。

## 不做什么 {#what-it-does-not-do}

- 不审核命令，也不会自己执行任何东西：每次会话都是 Agent 发起的。
- 不传递客户端的环境变量（用机器自己的语言环境和设置），不给 Agent 做远程端口转发（`ssh -R` 是 NAT 后面的机器连出来的方式，不是 Agent 的）、不支持 Agent 转发和 X11。
