---
sourceHash: sha256-d548969b
---

# 给 Agent 一台机器

读完这一页，Agent 就能通过 Beacon 的 SSH 通道登上一台服务器：它用自己的密钥登录 Beacon，Beacon 检查它能不能连这台机器，再用一张只为这一次连接签发、五分钟有效的证书连上去，整个会话录下来。Agent 手里始终没有这台机器的密钥或密码；收回权限时，它打开着的连接立即关闭。

你需要：Beacon 已经运行；一个预设为**值班**或**运维**（它们给 `machines:ssh`）、其所有者角色也允许的 Agent（[邀请你的团队](people.md)）。命令都在中心上执行；参考部署里是 `docker exec beacon beacon …`。

## 打开通道 {#turn-it-on}

参考部署已经替你做好了：`install.sh` 生成两把密钥，`config.yaml` 里也写好了。在别的环境里，自己生成两把密钥，写进配置：

1. **生成两把密钥**（通道自己的主机密钥，以及签发证书的 CA）：

   ```bash
   ssh-keygen -t ed25519 -N '' -C 'beacon host' -f secrets/ssh-host-key
   ssh-keygen -t ed25519 -N '' -C 'beacon ca' -f secrets/ssh-ca-key
   ```

2. **写进配置**，重启 Beacon：

   ```yaml
   ssh:
     listen: ":17002"
     public_address: ssh.example.com:17002   # Agent 连接的地址
     host_key_file: /etc/beacon/secrets/ssh-host-key
     ca_key_file: /etc/beacon/secrets/ssh-ca-key
     # source_addresses: [203.0.113.7/32]    # 设置后，证书只能从 Beacon 自己的地址使用
     keep: 4320h                             # 会话保留 180 天
   ```

3. **检查。** 通道是独立端口上的普通 SSH：直接开放这个端口，不要经过 HTTP 反向代理。`beacon check-config` 会解析两把密钥，两个配置项用了同一把也会拒绝；`beacon ssh status` 会显示：

   ```
   agents reach machines; the channel's host key is ssh-ed25519 AAAA…
   ```

拿到 CA 私钥的人，能登录每一台信任它的机器。请把它单独放在一个文件里，只让服务能读；Beacon 的地址固定的话，设置 `source_addresses`。通道保证什么、不保证什么，见[安全模型](../self-hosting/security.md)。

## 添加机器 {#add-a-machine}

1. **在中心上**写出机器名和地址。Beacon 连接 22 端口（或 `HOST:PORT`）一次，固定机器的主机密钥，并打印机器上要做的配置：

   ```bash
   docker exec beacon beacon machine add edge-1 edge-1.example.net -note "Los Angeles edge node"
   ```

   你会看到：

   ```
   added edge-1 at edge-1.example.net as beacon; its host key is ssh-ed25519 AAAA…
   On the machine, as root, if not done yet:
     echo 'ssh-ed25519 AAAA… beacon-ca' > /etc/ssh/beacon_ca.pub
     ...
   ```

2. **在机器上以 root** 执行它打印的那些行：

   ```bash
   echo 'ssh-ed25519 AAAA… beacon-ca' > /etc/ssh/beacon_ca.pub   # machine add 打印的那一行；beacon machine ca 可以再打印一次
   echo 'TrustedUserCAKeys /etc/ssh/beacon_ca.pub' > /etc/ssh/sshd_config.d/beacon.conf
   id beacon >/dev/null 2>&1 || useradd -m -s /bin/bash -p '*' beacon   # Beacon 证书里指定的账户
   systemctl reload ssh
   ```

3. **只给账户干活需要的权限。** 账户能做什么，才是真正的边界，比如在 `/etc/sudoers.d/beacon` 里写：

   ```
   beacon ALL=(root) NOPASSWD: /usr/bin/systemctl restart relay, /usr/bin/journalctl
   ```

`-user` 可以指定 `beacon` 以外的账户；`-critical` 把机器标记为关键机器，提醒在上面干活的人。OpenSSH 9.8 起，同一来源地址连接失败多了会被限制，而所有 Agent 都从 Beacon 的地址连过来：在机器的 `beacon.conf` 里加上 `PerSourcePenaltyExemptList 203.0.113.7`。`machine list` 列出所有机器；重装系统后用 `machine add … -replace` 重新固定主机密钥；`machine remove NAME` 移除一台机器，它的会话记录仍然保留。Beacon 连不进去的机器换个方向添加：见 [NAT 后面的机器](#machines-behind-nat)。

## 给 Agent 权限 {#give-an-agent-access}

Claude Code 这样用 OAuth 登录的交互式 Agent 会自己登记公钥：对它说 *「把我的 SSH 公钥 ~/.ssh/id_ed25519.pub 登记到 Beacon」*，它就会调用 `add_ssh_key`。无界面的 Agent 在创建时带上公钥：

```bash
docker exec beacon beacon agent add claude-oncall -owner alice -preset oncall \
  -machines edge-1,edge-2 -ssh-key "ssh-ed25519 AAAA… agent@workstation"
```

`-machines` 限定 Agent 能连的机器；不给就是全部。同一份凭据也用于 MCP 工具和 REST API。独立令牌的公钥和机器记在令牌上（`token ssh-key`、`token machines`）。

## 让 Agent 连上来 {#let-the-agent-connect}

两种方式，凭据相同：

- **用它自己的 ssh。** `list_machines` 会给出一段 `ssh_config` 和一行 `known_hosts`，供 Agent 自己的 `ssh`、`scp`、`sftp`、`rsync` 以及 `ssh -L` 端口转发使用：怎样放到位，见[接入 Claude Code](../integrations/claude-code.md#connect-with-your-own-ssh-scp-and-rsync)。
- **用 Beacon 的工具。** `run_command`、`read_file`、`write_file` 和 `edit_file` 的名字和用法都照着 Agent 自己的工具来；`list_machines` 列出机器和 Agent 能否连每台。每个参数见 [MCP 工具](../reference/mcp.md)；每次工具调用都单独算一次会话，和其他会话一样录下来。

每一次会话都有记录：一条命令、一个交互 shell、一次文件传输、一个转发端口，以及每次工具调用。录像（asciicast v3，存于 `data_dir/recordings/`）在控制台的会话页面回放（没有终端的会话，例如工具调用或 `ssh 主机 命令`，送来的行以单独的 `\n` 结尾，回放时按终端的显示方式换成 `\r\n`；文件和文字记录保留原样），文字记录里是 shell 的每条命令及其退出状态，告警群里每个 Agent 在每台机器上一条消息，附 **Replay** 链接。会话保留 `ssh.keep`（180 天），之后删除。

## 暂停，或停下某一个 Agent {#pause-or-stop-one-agent}

`beacon ssh pause` 立刻关闭所有正在进行的会话，并拒绝新的会话，直到 `beacon ssh resume`；`ssh status` 显示当前状态。只想停下某一个 Agent，就在它的页面上吊销凭据或删除它，或者 `beacon token revoke NAME`：它在机器上打开着的一切立即关闭。还有哪些开关有同样的效果，见[停下 Agent，暂停一切](stop.md)。

## NAT 后面的机器 {#machines-behind-nat}

有些机器 Beacon 连不进去：在 NAT 后面、在只许出不许进的防火墙后面、放在家里。这样的机器用它自带的 OpenSSH 客户端主动连到通道上，Beacon 再通过这条连接访问它的 sshd。机器上不用装任何东西。

1. **在机器上以 root** 生成它连出去用的密钥：

   ```bash
   ssh-keygen -t ed25519 -N '' -C 'beacon connector edge-9' -f /etc/ssh/beacon_connect_key
   ```

2. **在中心上**登记这把密钥（`-key` 填机器上 `/etc/ssh/beacon_connect_key.pub` 的内容）：

   ```bash
   docker exec beacon beacon machine add edge-9 connector -key "ssh-ed25519 AAAA… beacon connector edge-9" -note "office NAS"
   ```

   你会看到 `added edge-9 as a connector machine (user beacon): it connects out to Beacon with ssh -R; …`，以及一段给机器用的脚本：先和普通机器一样配置 CA 和 `beacon` 账户，再创建 `beacon-connector` 用户和一个带 `Restart=always` 的 systemd 单元。机器的主机密钥和其他机器一样要固定：用 `-host-key` 给出，或者让 Beacon 在第一次连接时自己学（审计日志和告警群都会说明）。

此后一切和普通机器相同：`ssh`、`scp`、`rsync`、工具、录像、暂停开关。`machine list` 显示 `connector: true` 和 `connected`；连接器没连上的机器立刻失败（`machine.disconnected`）；Beacon 重启后，各个单元几秒内重新连上。连接器密钥只能以它那台机器的身份登录，不能开会话；通道在这里保证什么，见[安全模型](../self-hosting/security.md)。

## 检查一下 {#check-it}

让 Agent 执行一条命令，再把会话读回来：

```
ssh edge-1 'hostname'
```

然后打开 `/console/sessions`（或者问 `list_sessions`）：会话和它的录像都在，告警群里也有那一行。通道拒绝时会说明原因：[Agent 连不上机器](../self-hosting/troubleshooting.md#the-agent-cannot-reach-a-machine)列出了每条消息和对应的处理办法。

## 下一步 {#next}

- [让 Agent 值班](on-call.md)：把 Agent 送上机器的证据，以及围绕它的整个循环。
- [复查 Agent 做了什么](console.md)：会话、文字记录和回放。
- [停下 Agent，暂停一切](stop.md)：各个刹车，以及每一个会关掉什么。
