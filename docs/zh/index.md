---
sourceHash: sha256-fdcf5b94
layout: home

hero:
  name: Beacon
  text: 让你的 AI Agent 值班
  tagline: 为管理服务器的 AI Agent 打造的自托管监控与 SSH 接入。Beacon 从用户所在的地方检测你的服务，出问题时带着证据叫醒你的 Agent，让它不拿你的密钥就能上服务器处理，并记下它执行的每一条命令。
  image:
    light: /logo.svg
    dark: /logo-dark.svg
    alt: Beacon
  actions:
    - theme: brand
      text: 30 分钟装好
      link: /zh/install
    - theme: alt
      text: 工作原理
      link: /zh/architecture
    - theme: alt
      text: GitHub
      link: https://github.com/openlinkos/beacon

features:
  - title: 从用户所在的地方检测
    details: 装在小机器上的检测程序检测 HTTP、TLS、TCP、镜像仓库、Git 和域名到期，并记下每次失败的原因。只有多个检测点都说坏了才算故障，单个检测点网络不好不会吵醒任何人。
    link: /zh/catalog
    linkText: 检测什么
  - title: 叫醒时证据一并给齐
    details: 签名的 webhook 或 GitHub workflow 叫醒你指定的 Agent。一次调用返回每个检测点的结果、同一故障域里的其他告警、故障前刚做的部署，以及已经做过的事。
    link: /zh/agents
    linkText: Agent 与告警
  - title: Agent 不拿密钥
    details: Agent 经 Beacon 用自己的 ssh、scp、rsync，或者用它的 MCP 工具。每次连接签一张五分钟有效的证书，服务器只信任 Beacon。可以单独吊销一个 Agent，也可以一个开关停下所有 Agent。
    link: /zh/ssh
    linkText: SSH 通道
  - title: 每条命令都有记录
    details: 会话能在浏览器里回放，交互 shell 拆成一条条命令和退出码，Agent 的备注写在告警上。事后复查做了什么，而不是事前逐条审批。
    link: /zh/pages
    linkText: 控制台
  - title: 修好要再测一遍
    details: 修完 Agent 让所有检测点立刻重测，一分钟内看到各自的结果。告警按检测点看到的情况解除，不按 Agent 说的。
    link: /zh/agents#probe-again-now
    linkText: 按需重测
  - title: 人一直在场
    details: 给用户看的公开状态页，给团队用的控制台，能在飞书卡片上认领和静默的告警，没人响应时升级到手机。
    link: /zh/status
    linkText: 状态页
  - title: 开源
    details: Go 编写，Apache License 2.0 许可，基于 VictoriaMetrics；一台 2 核 4 GB 的机器绰绰有余。Beacon 不调用任何模型，也不需要任何云账号。
    link: https://github.com/openlinkos/beacon
    linkText: openlinkos/beacon
---
