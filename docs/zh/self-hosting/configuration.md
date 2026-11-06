---
sourceHash: sha256-d4e0d1fe
---

# 配置

有两种东西都叫 Beacon 的配置，本页讲第一种：

- **`config.yaml`**，服务自己的配置：监听哪里、通过哪个聊天工具发告警、怎样升级到电话、SSH 通道的密钥。它是中心上的一个文件，Beacon 启动时读一次。
- **目录、检测目标和模块**——检测什么、从哪里检测。这些放在 Beacon 的数据库里，通过 API、MCP 或命令行修改；文件只用于导入和导出。那是[检测一个服务](../guide/monitoring.md)的内容；本页最后一节说明这些文件什么时候会被读。

示例以[装在服务器上](install.md)里的参考部署为准。

## config.yaml {#configyaml}

在参考部署里，它是中心上的 `/opt/beacon/config.yaml`，以只读方式挂进容器，路径为 `/etc/beacon/config.yaml`。每个字段在仓库根目录的 [`config.example.yaml`](https://github.com/openlinkos/beacon/blob/main/config.example.yaml) 里都有解释，并按键逐项列在 [config.yaml 参考](../reference/config.md)里；本节只给一张地图。

| 段 | 决定什么 |
| --- | --- |
| `listen`、`data_dir`、`catalog_dir` | HTTP 服务监听哪里（默认 `:17001`），数据库和录像放在哪里，目录文件从哪里导入 |
| `public_url`、`trusted_proxies`、`language`、`timezone` | 卡片和页面链接的地址；哪些代理可以设置 `X-Forwarded-For`；卡片和聊天消息的语言（`en` 或 `zh`）；显示用的时区 |
| `rules` | 规则引擎认证告警用的令牌文件。必填 |
| `routing` | 告警的分组与送达，相当于 Alertmanager 的 route：`group_by`、`group_wait`、`group_interval`、`resolve_timeout`、按服务的路由、抑制 |
| `victoriametrics` | 网关把结果写到这里，Beacon 从这里查询 |
| `gateway` | 机器的门：监听地址（默认 `:17006`，留空即关闭）、检测目标目录和模块文件 |
| `ssh` | SSH 通道：监听地址（默认 `:17002`）、Agent 连接的公开地址、主机密钥和 CA 密钥文件、证书允许的来源地址、会话和录像保留多久。设置了 `ca_key_file` 才开启 |
| `links` | 卡片上的 URL 模板：处理手册和仪表盘，按服务和告警替换 |
| `pages` | 卡片打开的签名链接有效多久 |
| `branding` | 公开页面上部署的标题，以及是否显示由 Beacon 提供 |
| `oauth` | Beacon 是否按 client-id 元数据文档接受 MCP 客户端（Claude Code 和 Codex 的都是）；连不了外网的部署把它关掉 |
| `feishu`、`pushover`、`bark`、`healthchecks` | 聊天、电话和失联报警；对应的段和文件存在之前都是关闭的 |
| `escalation` | 按严重级别：走哪个渠道、什么时候强提醒值班的人、什么时候打电话 |
| `ack_ttl`、`agents`、`changes` | 认领有效多久；新告警组出现时叫醒 Agent 的钩子；变更事件保留多久 |

不认识的键是错误，不是警告：拼错的段名会让 Beacon 起不来，`check-config`（下文）会先一步指出。时长写成 `30s`、`2m`、`4320h`。

## 密钥只放在文件里，不写在值里 {#secrets-are-files-never-values}

任何凭据都不写进 `config.yaml`：一律是一个 `*_file` 路径，Beacon 启动时读一次这个文件。密钥出现在 YAML 里就是配置错误；`*_file` 字段和它们指名的文件就是全部。

参考部署里这些文件放在 `/opt/beacon/secrets/` 下，属主 65532、权限 0400。`install.sh` 自己能生成的三个（`rules-token`、`ssh-host-key`、`ssh-ca-key`，永不离开这台机器）由它生成，你放进去的任何文件也会被它改成同样的属主和权限。聊天工具的那几个要你自己放：`feishu-app-id`、`feishu-app-secret`、`feishu-webhook-url`、`pushover-user-key`、`pushover-app-token`、`healthchecks-bridge-url`，[飞书页](../integrations/feishu.md)有完整过程。

密钥永远不进日志，也不出现在错误信息里。这些文件怎么填满是部署自己的事：Beacon 不依赖任何密钥管理器。

## 校验 {#checking-it}

`check-config` 校验 `config.yaml` 和它指名的一切，以及数据库里的配置，不启动任何东西。在参考部署里：

```bash
docker exec beacon beacon check-config
```

```
configuration OK: Feishu app false, Pushover false, Bark false; gateway ":17006"; SSH channel :17002; 0 agent hooks
files: 1 probe locations, 0 heartbeats, 0 resources, 3 probe targets in 2 sets, 5 modules, 1 status pages
database: version 2 in the running service: …
```

`install.sh` 每次启动前都会在自己的镜像里跑同一个校验，所以过不了的文件会让安装带着原因停下来，而不是让服务在半夜倒下。

## 让改动生效 {#applying-a-change}

`config.yaml` 只在启动时读一次。对它的改动要重启才能到 Beacon：

```bash
docker compose restart beacon
```

`install.sh` 发现 `config.yaml` 或某个密钥自它上次运行之后变了，会自己重启 Beacon，所以改配置的完整动作是：改文件，`sudo ./install.sh`，结束。

SIGHUP 不是这条路，这一点值得说清楚：**挂起信号不会重新读 `config.yaml`**。它只关系到目录文件（下一节）。聊天、升级到电话、Agent 钩子——这些都要重启。

最常见的场景是开聊天工具，三步：

1. 把每个密钥放进 `/opt/beacon/secrets/` 下对应的文件：`feishu-app-id`、`feishu-app-secret`、`feishu-webhook-url`；手机通知用 `pushover-user-key` 和 `pushover-app-token`；失联报警用 `healthchecks-bridge-url`。
2. 在 `/opt/beacon/config.yaml` 里取消 `feishu:` 段（以及 `pushover:`、`healthchecks:`）的注释，填上你的 `chat_id` 和值班的人。
3. 再运行一次 `sudo ./install.sh`：它会把密钥文件的属主和权限改对，校验所有文件，并重启 Beacon。之后日志里会出现 `feishu long connection ready`（`docker compose logs beacon`）。

## 目录文件：启动时与 SIGHUP {#the-catalogue-files-at-start-and-on-sighup}

第一次启动会把配置指名的文件（`catalog_dir`、`gateway.targets_dir`、`gateway.modules_file`）导入数据库，并记录日志 `imported the configuration files into the database, where it is changed from now on`；没通过校验的文件会让启动停止。从此数据库是唯一事实，文件只在被要求时才读：

- **SIGHUP**（`docker kill -s HUP beacon`）会导入它们，但前提是上次导入之后没有人通过 Beacon 改过配置，这样文件不能悄悄覆盖人和 Agent 刚做的修改。不满足时，日志会说明之后来过哪个改动、接下来怎么办：用 `config export` 把改动带回文件，或用 `config import` 覆盖它。
- **`beacon config import DIR`** 随时导入一个目录，先 `-dry-run`；**`beacon config export DIR`** 把生效的配置写回文件；**`beacon config history`** 和 **`beacon config revert VERSION`** 查看和撤销变更。

完整说明见[文件的导入与导出](../guide/monitoring.md#files-import-and-export)，包括导入的陷阱：文件里没有的条目会被删掉。
