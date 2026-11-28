---
sourceHash: sha256-dab62960
---

# 给 Agent 一台机器

读完这一页，Agent 就能通过 Beacon 的 SSH 通道登上一台服务器。它手里始终没有这台机器的密钥或密码。一次连接是这样的：

- Agent 用自己的密钥登录 Beacon；
- Beacon 检查它能不能连这台机器；
- Beacon 用一张只为这一次连接签发、五分钟有效的证书连上去；
- 每次会话都会记录连接人、目标和时间。

收回 Agent 的权限时，它打开着的连接立即关闭。

你需要：

- Beacon 已经运行。
- 一个预设为**值班**或**运维**的 Agent（它们给 `machines:ssh`），且其所有者的角色也允许（[邀请你的团队](people.md)）。

命令都在中心上执行。参考部署里是 `docker exec beacon beacon …`。

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

3. **开放端口并检查。** 通道是独立端口上的普通 SSH，所以直接开放这个端口，不要经过 HTTP 反向代理。`beacon check-config` 会解析两把密钥，两个配置项用了同一把也会拒绝。`beacon ssh status` 接着会显示：

   ```
   agents reach machines; the channel's host key is ssh-ed25519 AAAA…
   ```

拿到 CA 私钥的人，能登录每一台信任它的机器。请把它单独放在一个文件里，只让服务能读。Beacon 的地址固定的话，设置 `source_addresses`。通道保证什么、不保证什么，见[安全模型](../self-hosting/security.md)。

## 添加机器 {#add-a-machine}

1. **在中心上**写出机器名和地址。Beacon 连接 22 端口（或 `HOST:PORT`）一次，固定机器的主机密钥，并打印机器上要做的配置：

   ```bash
   docker exec beacon beacon machine add edge-1 edge-1.example.net -note "Los Angeles edge node"
   ```

   你会看到：

   ```
   added edge-1 at edge-1.example.net as beacon; its host key is ssh-ed25519 AAAA…
   On the machine, as root, if not done yet (Beacon's certificates are trusted for beacon alone):
     echo 'ssh-ed25519 AAAA… beacon-ca' > /etc/ssh/beacon_ca.pub
     ...
   ```

2. **在机器上以 root** 执行它打印的那些行：

   ```bash
   echo 'ssh-ed25519 AAAA… beacon-ca' > /etc/ssh/beacon_ca.pub   # machine add 打印的那一行；beacon machine ca 可以再打印一次
   printf 'Match User beacon\n  TrustedUserCAKeys /etc/ssh/beacon_ca.pub\nMatch all\n' > /etc/ssh/sshd_config.d/beacon.conf
   id beacon >/dev/null 2>&1 || useradd -m -s /bin/bash -p '*' beacon   # Beacon 证书里指定的账户
   systemctl reload ssh
   ```

   `Match User` 这一段只为这一个账户信任 Beacon 的证书颁发机构。其他账户（包括 `root`）的证书，无论 Beacon 签了什么，机器自己都会拒绝。用 `-user` 添加的机器，这里写那个账户；在 Beacon 里改了账户之后，用新账户重新执行这一行。

3. **只给账户干活需要的权限。** 账户能做什么，才是真正的边界。比如在 `/etc/sudoers.d/beacon` 里写：

   ```
   beacon ALL=(root) NOPASSWD: /usr/bin/systemctl restart relay, /usr/bin/journalctl
   ```

确认 `sshd_config` 已包含 `/etc/ssh/sshd_config.d/*.conf`。重新加载前先运行 `sshd -t`。Debian 和 Ubuntu 的服务名是 `ssh`，RHEL 等发行版使用 `sshd`。

如果机器上的 OpenSSH 支持 `PerSourcePenaltyExemptList`（9.8 引入），在 `beacon.conf` 开头、`Match` 那一行之前添加 `PerSourcePenaltyExemptList 203.0.113.7`，将示例地址换成中心的来源地址。Beacon 发起的所有连接都从这个地址到达。改完后验证配置并重新加载 sshd。

其他选项和命令：

- `-user` 指定 `beacon` 以外的账户。只有人或主机上的 `beacon` 能用别的账户添加机器，或之后改它，改成 `root` 只有主机上的 `beacon` 能做。
- `-critical` 把机器标记为关键机器。能访问全部机器的凭据，不点名就到不了关键机器；owner 能到。只有 owner 或主机上的 `beacon` 能标记关键机器，或修改、移除关键机器。机器成为关键机器之前就点名了它的凭据，最迟 30 天后到期（[机器访问](../reference/permissions.md#machine-access)）。
- `machine list` 列出所有机器。
- `machine add … -replace` 在重装系统后重新固定主机密钥。
- `machine remove NAME` 移除一台机器，它的会话记录仍然保留。在机器上也删掉 `/etc/ssh/beacon_ca.pub` 和 `Match User` 那一段，这样谁也不能重新添加它再访问它。

一台机器只有一条记录：不论谁来添加，Beacon 都拒绝在另一台机器的地址上、或用它的主机密钥添加机器（`machine.alias`）。从另一台机器的镜像克隆出来的机器，在生成自己的主机密钥之前（`rm /etc/ssh/ssh_host_*; ssh-keygen -A`，再重新加载 sshd），和那台机器共用主机密钥。Beacon 自己主机上的机器，即回环或链路本地地址、`localhost` 上的机器，只能在主机上用 `beacon machine add` 添加。Beacon 启动时，会在日志里列出在开始拒绝之前就被记录了两次的机器；把不该有的那条记录移除。

持有 `machines:write` 并能访问全部机器的人，在控制台的 **Machines** 页做同样的事：点名机器的凭据不能添加、修改或移除任何机器（[修改机器](../reference/permissions.md#changing-machines)）。**Add a machine** 填写名字、地址（`host` 或 `host:port`，或 NAT 后面）、账户（不填则是 `beacon`）、是否关键、备注；NAT 后面的机器还要填它连出来用的密钥，主机密钥可选。成功后页面只显示一次机器上要做的配置，和命令打印的是同一段。机器页面可以改备注、是否关键和账户，重新固定主机密钥（并显示旧指纹和新指纹），NAT 后面的机器还可以更换连出来用的密钥。**Remove the machine** 要求把机器名完整输入一遍。机器被忘掉，打开的连接器会关掉，会话记录仍然保留。

Beacon 连不进去的机器，换个方向添加：见 [NAT 后面的机器](#machines-behind-nat)。

## 检查机器 SSH {#check-machine-ssh}

配置并重新加载 sshd 后，在**机器 → 通过 SSH 连接**中打开已登记的机器，选择**检查机器 SSH**。你需要值班角色，或另一个允许对这台机器使用 SSH 的角色。Beacon 检查登记的连接路径与固定主机密钥，并尝试用短期证书登录，期间不会打开 shell 或执行命令。登录后可看到检查停在哪一步；审计日志以 `machine_ssh_check` 记录这次检查。

**机器 SSH 已就绪**表示 Beacon 到机器的握手通过。接着用下文的个人连接验证工作站能否访问，以及个人密钥能否登录。证书登录未完成时，检查机器账号、`TrustedUserCAKeys` 和 sshd 日志。主机密钥不匹配时，先核对机器指纹再更新固定密钥。NAT 后的机器需要让连接器登记到正确 sshd 端口的转发。

## 以自己的身份连接 {#connect-as-yourself}

有值班或更高角色时，在**你的账号 → 个人 SSH 密钥**登记专供个人账号使用的公钥。需要新密钥时，在工作站上生成。

```bash
mkdir -p -m 700 ~/.ssh
ssh-keygen -t ed25519 -f ~/.ssh/beacon-personal
```

只登记 `~/.ssh/beacon-personal.pub` 的内容，配套私钥留在工作站。Beacon 只允许把同一个密钥指纹登记给一个身份，所以同一台工作站上的 Agent 也需要另用一把密钥。

打开**机器 → edge-1 → 通过 SSH 连接**。把页面给出的 `Host` 段复制到 `~/.ssh/beacon-personal.conf`，将主机密钥行追加到 `~/.ssh/known_hosts`。如果私钥保存在其他路径，请修改 `IdentityFile`。运行 `ssh -F ~/.ssh/beacon-personal.conf beacon-edge-1 hostname`。这份独立配置会选用你的密钥，并以个人账号建立新的连接。参考部署会设置 `ssh.public_address`，让控制台能显示这些配置。

打开**会话**，找到 `edge-1` 上刚执行的命令并开始回放。操作人应显示为你的用户名。Agent 的会话记为 `人 / Agent`。在**你的账号**移除个人公钥，会关闭它已打开的连接，并阻止它再次登录。

## 给 Agent 权限 {#give-an-agent-access}

用 OAuth 登录的交互式 Agent 可以直接调用 `run_command`，无需登记 SSH 密钥。如果要用自己的 OpenSSH 客户端，按[用自己的 ssh、scp 和 rsync 连接](../integrations/claude-code.md#connect-with-your-own-ssh-scp-and-rsync)登记专用的 Agent 密钥。

无界面的 Agent 在创建时带上公钥：

```bash
docker exec beacon beacon agent add claude-oncall -owner alice -preset oncall \
  -machines edge-1,edge-2 -ssh-key "ssh-ed25519 AAAA… agent@workstation"
```

`-machines` 点名 Agent 能连的机器，或写 `all`：全部非关键机器，含以后接入的。不给就一台也连不了。关键机器只有点名才能到（`-machines all,ca-1`），只有 owner 或主机上的 `beacon` 能点名；Agent 对它的访问默认 30 天后到期，可以另给期限（`ca-1@72h`、`ca-1@never`）。之后用 `beacon agent machines NAME …` 修改。同一份凭据也用于 MCP 工具和 REST API。独立令牌的公钥和机器记在令牌上（`token ssh-key`、`token machines`）。规则见[机器访问](../reference/permissions.md#machine-access)。

## 让 Agent 连上来 {#let-the-agent-connect}

两种方式，凭据相同：

- **用它自己的 ssh。** `list_machines` 会给出一段 `ssh_config` 和一行 `known_hosts`。它们供 Agent 自己的 `ssh`、`scp`、`sftp`、`rsync` 以及 `ssh -L` 端口转发使用。怎样放到位，见[接入 Claude Code](../integrations/claude-code.md#connect-with-your-own-ssh-scp-and-rsync)。
- **用 Beacon 的工具。** `run_command`、`read_file`、`write_file` 和 `edit_file` 的名字和用法都照着 Agent 自己的工具来。`list_machines` 列出机器，以及 Agent 能否连每台。每个参数见 [MCP 工具](../reference/mcp.md)。每次工具调用都单独算一次会话，和其他会话一样录下来。

Beacon 为命令、shell、文件传输、端口转发和每次工具调用保留会话记录。采集内容取决于会话类型。

- **录像**在会话支持时采集终端活动，输入和输出都按原样记录。`data_dir/recordings/` 下可用的 asciicast v3 文件可在会话页面回放。
- **文字记录**保留命令或工具的输入和输出，或具备可用命令集成的 shell 中每条命令及其退出状态。
- **SFTP 和端口转发**保留连接信息。控制台分别显示录像和文字记录的采集状态，包括采集不完整和存储故障。
- **告警群**里每个 Agent 在每台机器上一条消息，附 **Replay** 链接，读者用自己的 Beacon 账号查看。

会话保留 `ssh.keep`（180 天），之后删除。

一个会话的录像最多 16 MiB。没有记录的内容一律不放行：下一段输入或输出放不进录像时，Beacon 关闭会话，并在 stderr 上告诉 Agent 原因。录像以一个标记结尾，会话记为在上限处停止。这样停下的工具调用返回 `ssh.recording_limit`。大文件用 `scp` 或 `rsync` 复制，长输出用 `head`、`tail` 或 `grep` 截短。

shell 的命令和退出状态按输出里的 shell 集成标记切分。shell 运行的任何程序都能打印这种标记，所以这份列表只作参考，并如此标明（`commands_advisory`）。录像保留键入的内容和机器原样送出的输出，标记也在其中。

没有终端的会话，例如工具调用或 `ssh 主机 命令`，送来的行以单独的 `\n` 结尾。回放时按终端的显示方式换成 `\r\n`。文件和文字记录保留原样。

## 暂停，或停下某一个 Agent {#pause-or-stop-one-agent}

`beacon ssh pause` 立刻关闭所有正在进行的会话，并拒绝新的会话，直到 `beacon ssh resume`。`ssh status` 显示当前状态。有 `machines:write` 的都能暂停通道；只有人，或主机上的 `beacon ssh resume`，才能恢复。

只想停下某一个 Agent，就在它的页面上吊销凭据或删除它，或者执行 `beacon token revoke NAME`。它在机器上打开着的一切立即关闭。还有哪些开关有同样的效果，见[停下 Agent，暂停一切](stop.md)。

连接始终绑定在它打开时的那台机器上。删除机器、重新添加机器或修改它的登录账号，都会关闭在它上面打开着的一切；多路复用的连接也不会在改动后的机器上打开新会话。

## NAT 后面的机器 {#machines-behind-nat}

有些机器 Beacon 连不进去：在 NAT 后面、在只许出不许进的防火墙后面，或者放在家里。这样的机器用它自带的 OpenSSH 客户端主动连到通道上。Beacon 再通过这条连接访问它的 sshd。机器上不用装任何东西。

1. **在机器上以 root** 生成它连出去用的密钥：

   ```bash
   ssh-keygen -t ed25519 -N '' -C 'beacon connector edge-9' -f /etc/ssh/beacon_connect_key
   ```

2. **在中心上**登记这把密钥（`-key` 填机器上 `/etc/ssh/beacon_connect_key.pub` 的内容）：

   ```bash
   docker exec beacon beacon machine add edge-9 connector -key "ssh-ed25519 AAAA… beacon connector edge-9" -note "office NAS"
   ```

   你会看到 `added edge-9 as a connector machine (user beacon): it connects out to Beacon with ssh -R; …`，以及一段给机器用的脚本。脚本先和普通机器一样配置 CA 和 `beacon` 账户，再创建 `beacon-connector` 用户和一个带 `Restart=always` 的 systemd 单元。

   机器的主机密钥和其他机器一样要固定。用 `-host-key` 给出，或者让 Beacon 在第一次连接时自己学（审计日志和告警群都会说明）。

   每台机器用自己的密钥连出来：别的机器已经登记的密钥，Beacon 会拒绝。从镜像克隆的机器带着原机的密钥，要在克隆机上新生成一把。一条连接只在机器仍有它登录时的密钥和登记时才承载这台机器。重新添加机器、换密钥，或把它改成 Beacon 直连的机器，都会关闭这条连接，systemd 单元会重新连上来。

此后一切和普通机器相同：`ssh`、`scp`、`rsync`、工具、录像、暂停开关。

- `machine list` 显示 `connector: true` 和 `connected`。
- 连接器没连上的机器立刻失败（`machine.disconnected`）。
- Beacon 重启后，各个单元几秒内重新连上。
- 连接器密钥只能以它那台机器的身份登录，不能开会话。

通道在这里保证什么，见[安全模型](../self-hosting/security.md)。

## 检查一下 {#check-it}

1. **让 Agent 执行一条命令：**

   ```
   ssh -F ~/.ssh/beacon-claude-code.conf edge-1 hostname
   ```

2. **查看会话。** 打开 `/console/sessions`，或调用 `list_sessions`。会话带有回放记录，操作人应显示为 `人 / Agent`。配置了告警群时，群里也会收到回放链接，读者登录后按权限查看。

通道拒绝时会说明原因。[Agent 连不上机器](../self-hosting/troubleshooting.md#the-agent-cannot-reach-a-machine)列出了每条消息和对应的处理办法。审计日志把拒绝记为 `ssh_refused`，带上机器和原因；同一身份、机器和原因每分钟记一次。

## 下一步 {#next}

- [让 Agent 值班](on-call.md)：把 Agent 送上机器的证据，以及围绕它的整个循环。
- [复查 Agent 做了什么](console.md)：会话、文字记录和回放。
- [停下 Agent，暂停一切](stop.md)：各个刹车，以及每一个会关掉什么。
