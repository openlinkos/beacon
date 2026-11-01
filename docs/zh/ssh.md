# SSH 通道

[English](../ssh.md) | 简体中文

Agent 经 Beacon 到达机器：用它自己的 `ssh`、`scp`、`sftp`、`rsync` 和 `ssh -L`，或者用 MCP 工具（`run_command`、`read_file`、`write_file`、`edit_file`）。Agent 从来拿不到机器的密码或密钥。Beacon 为每次连接签一张 5 分钟有效的证书，机器只信任 Beacon 的 CA。每次会话都录制下来可以回放，交互 shell 拆成一条条命令并带退出码，并回显到告警群。

Beacon 不审核命令：执行什么由 Agent 自己判断，由它自己的工具链来做。人看得到一切，也能暂停整个通道。

## 打开

生成两把密钥，写进配置：

```bash
ssh-keygen -t ed25519 -N '' -C 'beacon host' -f secrets/ssh-host-key   # 通道自己的主机密钥
ssh-keygen -t ed25519 -N '' -C 'beacon ca' -f secrets/ssh-ca-key       # 用来签证书
```

```yaml
ssh:
  listen: ":17002"
  public_address: ssh.example.com:17002   # Agent 连接的地址
  host_key_file: /etc/linkos-beacon/secrets/ssh-host-key
  ca_key_file: /etc/linkos-beacon/secrets/ssh-ca-key
  # source_addresses: [203.0.113.7/32]    # 证书只能从 Beacon 自己的地址使用
  keep: 4320h                             # 会话保留 180 天
```

通道是独立端口上的普通 SSH：直接开放这个端口（防火墙规则），不要经过 HTTP 反向代理。`linkos-beacon check-config` 会检查两把密钥。

CA 私钥能登录每一台信任它的机器。单独放一个文件，只有服务能读；Beacon 的地址固定时，配上 `source_addresses`。

## 添加机器

```bash
docker exec linkos-beacon linkos-beacon machine add edge-1 edge-1.example.net -note "洛杉矶边缘节点"
```

Beacon 连接一次，固定机器的主机密钥，并打印机器需要做的配置。在机器上以 root 执行：

```bash
docker exec linkos-beacon linkos-beacon machine ca > /etc/ssh/beacon_ca.pub   # 或者粘贴它打印的那一行
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

Beacon 连不进去的 NAT 后面的机器暂不支持；计划做一个主动连出的小程序。

## 给 Agent 权限

一个带 `machines:ssh` 作用域和 Agent 公钥的令牌：

```bash
docker exec linkos-beacon linkos-beacon token create claude-code \
  -scopes status:read,machines:ssh,sessions:read -expires 90d -ssh-key "$(cat ~/.ssh/id_ed25519.pub)"
```

`-machines edge-1,edge-2` 限定它能到达的机器；`token machines NAME all` 取消限定。以后用 `token ssh-key NAME add KEY` 和 `remove FINGERPRINT` 增删公钥。

同一个令牌也用于 MCP，工具有：

| 工具 | 做什么 |
| --- | --- |
| `list_machines` | 机器列表、令牌能否到达每台，以及给 Agent 自己的 ssh 用的 `ssh_config` 配置段和 `known_hosts` 行 |
| `run_command` | 执行一条命令（在 `sh` 里、以机器上的账号、不带终端）：返回退出码，以及纯文本的 stdout 和 stderr |
| `read_file` | 读文本文件的若干行，像 `cat -n` 那样带行号，可用 `offset` 和 `limit` |
| `write_file` | 写整个文件，最大 1 MiB，目录不存在时自动创建 |
| `edit_file` | 精确替换一段文字，一处或全部；文件在这期间被改过就拒绝 |
| `list_sessions`、`get_session` | 谁在哪里做了什么；某次会话的记录 |
| `add_machine`、`remove_machine`、`pause_ssh`、`resume_ssh` | 需要 `machines:write` |

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

## 记录什么

每一次会话：一条命令、一个交互 shell、一次文件传输、一个转发端口，以及每一次工具调用。

- 录制格式是 asciicast v3，存在 `data_dir/recordings/YYYY/MM/DD/` 下，可以用 `asciinema play` 播放，或在网页里用 asciinema-player 播放。`GET /api/v1/sessions/{id}/recording` 可以下载。
- 会话记录（`get_session`）里有命令的纯文本输出；交互 shell 则是每条命令及其输出和退出码。Beacon 启动 bash 时带上 shell 集成标记，由 bash 报出每条命令行，所以提前输入的命令也认得出来（这个 shell 会清空 `HISTCONTROL`，每条命令也都会进账号的历史记录）；其他 shell 照常录制，只是不拆分命令。
- 输入的内容也会录下来。Agent 从不输入密码；人也不要在会话里输入密码。
- 告警群里，每个 Agent 在每台机器上做的事最多每分钟汇成一条消息：执行了什么、退出码是多少。
- 会话保留 `ssh.keep` 这么久，之后连同文件一起删除。

## 暂停

`linkos-beacon ssh pause`（或者 `pause_ssh`，或者 `POST /api/v1/ssh/pause`）关闭所有正在进行的会话，并拒绝新的会话，直到 `ssh resume`。`ssh status` 显示当前状态。

## 不做什么

- 不审核命令，也不会自己执行任何东西：每次会话都是 Agent 发起的。
- 不传递客户端的环境变量（用机器自己的语言环境和设置），不支持远程端口转发（`ssh -R`）、Agent 转发和 X11。
