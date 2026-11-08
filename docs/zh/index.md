---
sourceHash: sha256-9cebf760
layout: home
title: 给 AI Agent 的基础设施，一切可追溯
---

<OlHero command="git clone https://github.com/openlinkos/beacon.git && cd beacon/deploy/try && sh up.sh">
  <template #title>给 AI Agent 的基础设施，一切可追溯</template>
  <template #lead>你的 Agent 经 Beacon 登上机器、盯住服务，很快也在这里取用授权给它的凭据。你的团队在同一个地方做同样的事，双方做的每件事都有记录。</template>
  <template #actions>
    <a href="/zh/guide/quickstart">10 分钟试用</a>
    <a class="ghost" href="/zh/guide/concepts">工作原理</a>
    <a class="ghost" href="https://github.com/openlinkos/beacon">GitHub</a>
  </template>
  <template #meta>十分钟、一条命令、本机的 Docker——不需要域名、证书，也不需要云账号。</template>
  <template #media>
    <OlCast src="/media/agent-repairs-demo-web.cast" player="/landing/asciinema-player.min.js" player-css="/landing/asciinema-player.css" poster="/landing/cast-poster.svg" alt="终端里 Agent 修复演示网页服务：健康检查先是连接被拒，Agent 执行 start-web，健康检查返回 ok" caption="在试用环境里录下的：一个 Agent 经 SSH 通道，用自己已有的工具修好演示服务。" />
  </template>
</OlHero>

## 一次故障，从头到尾 {#one-outage}

上面的录像是这几部分合起来做到的一个闭环。在试用环境里你弄坏一个演示服务，告警、修复、复测和回放随之而来。下面是它的五步。凌晨三点，它们在没人盯着的时候照常运转。

<OlSteps :steps="[
  { title: '多个检测点同时看到故障', text: '一个不到 20 MB 的检测程序装在任何小机器上，按用户访问的路径检测每个服务，并记下每次失败的原因。多个检测点都说坏了才算故障，单个检测点网络不好不会吵醒任何人。', media: '/landing/step-probes.svg', alt: '一项服务出故障时产生的证据：服务、目标、检测点和原因' },
  { title: '告警带着证据叫醒 Agent', text: '签名的 webhook 或 GitHub workflow 叫醒你指定的 Agent。一次调用返回每个检测点看到了什么、同一故障域里的其他告警、故障前刚做的部署，以及已经有谁做过什么。', media: '/landing/step-wake.svg', alt: '把证据交给 Agent 的那次调用：检测结果、相关告警、变更和已做的事' },
  { title: 'Agent 不持有密钥，经通道上机器', text: '它经 Beacon 用自己的 ssh、scp、rsync，或者用执行命令、编辑文件的 MCP 工具。每次连接签一张五分钟有效的证书，机器只信任 Beacon。', media: '/media/console-replay-light.png', alt: '控制台里的会话回放：Agent 执行了什么，返回了什么' },
  { title: 'probe_now 复测', text: 'Agent 让所有检测点立刻重测，在等待时间内看到各自的答案。告警按检测点看到的情况解除，不按 Agent 说的。', media: '/landing/step-probe-now.svg', alt: '所有检测点同时重测，答案逐个返回' },
  { title: '告警恢复，人在控制台回放', text: '检测恢复后告警组随即解除，Agent 的备注留在时间线上。在控制台里，每次会话都能在浏览器里逐条命令回放。', media: '/media/console-alert-light.png', alt: '修复后的告警组：带 Agent 备注的时间线、恢复的检测' },
]" />

## Agent 和人用同一个地方 {#one-place}

每一部分做的，都是人已经有工具在做的一件事。Agent 按它已有的方式来用，人用起来至少和那件工具一样好。

### 团队密码管理器 {#a-team-password-manager}

人在这里保存、共享密码、密钥和令牌。Agent 凭授权使用，可以只用不看，每次使用都连同理由记录下来。正在开发中，[目标](/zh/about/goals)里有它的样子。

### Agent 的可信访问 {#trusted-access-for-agents}

每个 Agent 都是一个身份，代表某个人行事，不超出这个人的角色。Claude Code 和 Codex 在浏览器里登录；无界面的 Agent 用一个能自动续期的令牌。[邀请你的团队](/zh/guide/people)。

### SSH 跳板机：Agent 不持有密钥 {#the-agent-never-holds-a-key}

每次连接签一张五分钟有效的证书，机器只信任 Beacon。可以单独吊销一个 Agent，也可以一个开关停下所有 Agent。[给 Agent 一台机器](/zh/guide/machines)。

### 监控：从用户所在的地方检测 {#checked-from-where-your-users-are}

检测程序检查 HTTP、TLS、TCP、镜像仓库、Git 和域名到期，并说清失败的原因：解析、连接、TLS、超时还是状态码。检测目标放在 Beacon 里，Agent 能自己把开发的服务加进检测。[检测一个服务](/zh/guide/monitoring)。

### 每个动作都有记录 {#every-command-on-record}

会话能在浏览器里回放，交互 shell 拆成一条条命令和退出码，Agent 的备注写在告警上。每次修改都追溯到是谁做的、代表谁、为什么。事后复查做了什么，而不是事前逐条审批。[复查 Agent 做了什么](/zh/guide/console)。

## 接入 {#works-with}

Claude Code、Codex 在浏览器里经 MCP 的 OAuth 登录，任何 MCP 客户端指向 `/mcp` 即可，无界面 Agent 由 CI 里一条签名的 webhook 叫醒。

<OlLogos label="Agent 客户端与唤醒方式" :items="[
  { name: 'Claude Code', href: '/zh/integrations/claude-code' },
  { name: 'Codex', href: '/zh/integrations/codex' },
  { name: 'Cursor', href: '/zh/integrations/cursor' },
  { name: '任意 MCP 客户端', href: '/zh/integrations/mcp-clients' },
  { name: 'Webhook', href: '/zh/integrations/webhooks' },
  { name: 'GitHub Actions', href: '/zh/integrations/github-actions' },
  { name: '飞书', href: '/zh/integrations/feishu' },
]" />

## 文档 {#docs}

| 部分 | 内容 |
| --- | --- |
| [指南](/zh/guide/) | Beacon 是什么、在本机十分钟试用、它如何工作，然后[每件事一页](/zh/guide/using-beacon)：监控与告警、Agent 值班、机器与 SSH 通道、团队与权限 |
| [接入](/zh/integrations/) | Claude Code、Codex、Cursor 等 Agent 客户端；webhook、GitHub Actions、变更事件和飞书 |
| [自托管](/zh/self-hosting/) | 装到服务器上、检测点、配置、升级、备份和安全 |
| [参考](/zh/reference/) | 每个接口、工具、命令、配置项和作用域 |
| [关于](/zh/about/) | 目标、架构和更新日志 |

## 自托管，开源 {#self-hosted}

<OlBand>
  <template #title>一台机器就能跑</template>
  <template #text>一台 2 核 4 GB 的 Linux 机器绰绰有余。Apache-2.0 许可；Beacon 不调用任何模型，也不需要云账号。</template>
  <template #actions>
    <a href="/zh/self-hosting/install">装到服务器</a>
    <a class="ghost" href="https://github.com/openlinkos/beacon">GitHub</a>
  </template>
</OlBand>

装到服务器上，带 TLS 和两个域名：

```bash
git clone https://github.com/openlinkos/beacon.git
sudo cp -r beacon/deploy /opt/beacon && cd /opt/beacon
sudo ./install.sh
```

## 正在运行 {#running-now}

做 Beacon 的人自己的运维就跑在 Beacon 上。LinkOS 服务的公开状态页 [status.linkos.io](https://status.linkos.io) 由 Beacon 提供：每项服务的状态、90 天的历史和事件记录，和你的用户读到的页面一样。
