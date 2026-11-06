import { readFileSync } from 'node:fs';
import { defineOpenLinkOSConfig, type OpenLinkOSConfig } from '@openlinkos/vitepress-theme/config';
import { checkReference } from './reference';

const repo = 'https://github.com/openlinkos/beacon';

/**
 * The version these docs describe: the newest version in CHANGELOG.md, with its latest release
 * candidate while it has only candidates ("## 0.3.0", then "### rc.6 (…)" gives v0.3.0-rc.6).
 */
const version = ((): string | null => {
  const log = readFileSync(new URL('../../CHANGELOG.md', import.meta.url), 'utf8');
  const lines = log.split(/\r?\n/);
  const start = lines.findIndex((l) => /^## \[?\d+\.\d+\.\d+\]?(\s|$)/.test(l));
  if (start < 0) return null;
  const release = /^## \[?(\d+\.\d+\.\d+)/.exec(lines[start])?.[1];
  const next = lines.slice(start + 1).find((l) => /^##{1,2} /.test(l));
  const rc = next && /^### rc\.(\d+)\b/.exec(next)?.[1];
  return `v${release}${rc ? `-rc.${rc}` : ''}`;
})();

/** GitHub's heading ids, so that a link to a heading works the same on GitHub and on the site. */
const githubSlug = (title: string): string =>
  title
    .trim()
    .toLowerCase()
    .replace(/[^\p{L}\p{M}\p{N}\p{Pc} -]/gu, '')
    .replace(/ /g, '-');

const site: Omit<OpenLinkOSConfig, 'brand'> = {
  hostname: 'https://beacon.openlinkos.com',
  title: 'Beacon',
  lastUpdated: false,
  markdown: { anchor: { slugify: githubSlug } },
  themeConfig: {
    socialLinks: [{ icon: 'github', link: repo }],
  },
  legal: [
    {
      text: { en: 'Apache License 2.0', zh: 'Apache 许可证 2.0' },
      link: `${repo}/blob/main/LICENSE`,
    },
  ],
  // The reference pages list every route, error code, scope and MCP tool the code has.
  buildEnd: () => {
    const problems = checkReference();
    if (problems.length) {
      throw new Error(
        `The reference is out of step with the code:\n${problems.map((p) => `  - ${p}`).join('\n')}`,
      );
    }
  },
  locales: {
    root: {
      description: 'Self-hosted monitoring and SSH access for the AI agents that run your servers.',
      themeConfig: {
        nav: [
          { text: 'Guide', link: '/guide/' },
          {
            text: 'Integrations',
            items: [
              { text: 'Claude Code', link: '/integrations/claude-code' },
              { text: 'Codex', link: '/integrations/codex' },
              { text: 'Cursor', link: '/integrations/cursor' },
              { text: 'Other MCP clients', link: '/integrations/mcp-clients' },
              { text: 'Webhooks', link: '/integrations/webhooks' },
              { text: 'GitHub Actions', link: '/integrations/github-actions' },
              { text: 'Headless agents and CI', link: '/integrations/headless' },
              { text: 'Feishu', link: '/integrations/feishu' },
              { text: 'Change events', link: '/integrations/change-events' },
              { text: 'The on-call prompt', link: '/integrations/on-call-prompt' },
            ],
          },
          { text: 'Self-hosting', link: '/self-hosting/' },
          {
            text: 'Reference',
            items: [
              { text: 'Command line', link: '/reference/cli' },
              { text: 'config.yaml', link: '/reference/config' },
              { text: 'The catalogue', link: '/reference/catalogue' },
              { text: 'REST API', link: '/reference/api' },
              { text: 'MCP tools', link: '/reference/mcp' },
              { text: 'Webhooks', link: '/reference/webhooks' },
              { text: 'Roles, presets and scopes', link: '/reference/permissions' },
              { text: 'Metrics and alert rules', link: '/reference/metrics' },
              { text: 'Sources', link: '/reference/sources' },
            ],
          },
          {
            text: version ?? 'Releases',
            items: [
              { text: 'Changelog', link: '/changelog' },
              { text: 'Releases', link: `${repo}/releases` },
              { text: 'Contributing', link: `${repo}/blob/main/CONTRIBUTING.md` },
              { text: 'Security policy', link: `${repo}/blob/main/SECURITY.md` },
            ],
          },
        ],
        sidebar: [
          {
            text: 'Guide',
            items: [
              { text: 'What is Beacon', link: '/guide/' },
              { text: 'Try it on your laptop', link: '/guide/quickstart' },
              { text: 'How Beacon works', link: '/guide/concepts' },
              { text: 'Put an agent on call', link: '/guide/on-call' },
              { text: 'Give agents a machine', link: '/guide/machines' },
              { text: 'Monitor a service', link: '/guide/monitoring' },
              { text: 'Heartbeats and expiring resources', link: '/guide/heartbeats' },
              { text: 'Publish a status page', link: '/guide/status-pages' },
              { text: 'Invite your team', link: '/guide/people' },
              { text: 'Review what agents did', link: '/guide/console' },
              { text: 'Rehearse an outage', link: '/guide/drills' },
              { text: 'Stop an agent, pause everything', link: '/guide/stop' },
            ],
          },
          {
            text: 'Integrations',
            items: [
              { text: 'Claude Code', link: '/integrations/claude-code' },
              { text: 'Codex', link: '/integrations/codex' },
              { text: 'Cursor', link: '/integrations/cursor' },
              { text: 'Other MCP clients', link: '/integrations/mcp-clients' },
              { text: 'Agents woken by a webhook', link: '/integrations/webhooks' },
              { text: 'GitHub Actions', link: '/integrations/github-actions' },
              { text: 'Headless agents and CI', link: '/integrations/headless' },
              { text: 'Set up the Feishu app', link: '/integrations/feishu' },
              { text: 'Deployments and Flux', link: '/integrations/change-events' },
              { text: 'The on-call prompt', link: '/integrations/on-call-prompt' },
            ],
          },
          {
            text: 'Self-hosting',
            items: [
              { text: 'Deployment options', link: '/self-hosting/' },
              { text: 'On a server', link: '/self-hosting/install' },
              { text: 'Probe locations', link: '/self-hosting/probes' },
              { text: 'Configuration', link: '/self-hosting/configuration' },
              { text: 'Upgrades', link: '/self-hosting/upgrade' },
              { text: 'PostgreSQL', link: '/self-hosting/postgres' },
              { text: 'Backup and restore', link: '/self-hosting/backup' },
              { text: 'Security model', link: '/self-hosting/security' },
              { text: 'Troubleshooting', link: '/self-hosting/troubleshooting' },
            ],
          },
          {
            text: 'Reference',
            items: [
              { text: 'Command line', link: '/reference/cli' },
              { text: 'config.yaml', link: '/reference/config' },
              { text: 'The catalogue', link: '/reference/catalogue' },
              { text: 'REST API', link: '/reference/api' },
              { text: 'MCP tools', link: '/reference/mcp' },
              { text: 'Webhooks', link: '/reference/webhooks' },
              { text: 'Roles, presets and scopes', link: '/reference/permissions' },
              { text: 'Metrics and alert rules', link: '/reference/metrics' },
              { text: 'Sources', link: '/reference/sources' },
            ],
          },
          {
            text: 'About',
            items: [
              { text: 'Goals', link: '/about/goals' },
              { text: 'Architecture', link: '/about/architecture' },
              { text: 'Changelog', link: '/changelog' },
            ],
          },
        ],
      },
    },
    zh: {
      description: '为管理服务器的 AI Agent 打造的自托管监控与 SSH 接入。',
      themeConfig: {
        nav: [
          { text: '指南', link: '/zh/guide/' },
          {
            text: '接入',
            items: [
              { text: 'Claude Code', link: '/zh/integrations/claude-code' },
              { text: 'Codex', link: '/zh/integrations/codex' },
              { text: 'Cursor', link: '/zh/integrations/cursor' },
              { text: '其他 MCP 客户端', link: '/zh/integrations/mcp-clients' },
              { text: 'Webhook', link: '/zh/integrations/webhooks' },
              { text: 'GitHub Actions', link: '/zh/integrations/github-actions' },
              { text: '无头 Agent 与 CI', link: '/zh/integrations/headless' },
              { text: '飞书', link: '/zh/integrations/feishu' },
              { text: '变更事件', link: '/zh/integrations/change-events' },
              { text: '值班提示词', link: '/zh/integrations/on-call-prompt' },
            ],
          },
          { text: '自托管', link: '/zh/self-hosting/' },
          {
            text: '参考',
            items: [
              { text: '命令行', link: '/zh/reference/cli' },
              { text: 'config.yaml', link: '/zh/reference/config' },
              { text: '目录文件格式', link: '/zh/reference/catalogue' },
              { text: 'REST API', link: '/zh/reference/api' },
              { text: 'MCP 工具', link: '/zh/reference/mcp' },
              { text: 'Webhook', link: '/zh/reference/webhooks' },
              { text: '角色、预设与权限', link: '/zh/reference/permissions' },
              { text: '指标与告警规则', link: '/zh/reference/metrics' },
              { text: '数据来源', link: '/zh/reference/sources' },
            ],
          },
          {
            text: version ?? '发布页',
            items: [
              { text: '更新日志', link: '/zh/changelog' },
              { text: '发布页', link: `${repo}/releases` },
              { text: '参与贡献', link: `${repo}/blob/main/CONTRIBUTING.md` },
              { text: '安全政策', link: `${repo}/blob/main/SECURITY.md` },
            ],
          },
        ],
        sidebar: [
          {
            text: '指南',
            items: [
              { text: 'Beacon 是什么', link: '/zh/guide/' },
              { text: '在本机试一试', link: '/zh/guide/quickstart' },
              { text: 'Beacon 如何工作', link: '/zh/guide/concepts' },
              { text: '让 Agent 值班', link: '/zh/guide/on-call' },
              { text: '给 Agent 一台机器', link: '/zh/guide/machines' },
              { text: '检测一个服务', link: '/zh/guide/monitoring' },
              { text: '心跳与到期资源', link: '/zh/guide/heartbeats' },
              { text: '发布状态页', link: '/zh/guide/status-pages' },
              { text: '邀请你的团队', link: '/zh/guide/people' },
              { text: '复查 Agent 做了什么', link: '/zh/guide/console' },
              { text: '演练一次故障', link: '/zh/guide/drills' },
              { text: '停下 Agent，暂停一切', link: '/zh/guide/stop' },
            ],
          },
          {
            text: '接入',
            items: [
              { text: 'Claude Code', link: '/zh/integrations/claude-code' },
              { text: 'Codex', link: '/zh/integrations/codex' },
              { text: 'Cursor', link: '/zh/integrations/cursor' },
              { text: '其他 MCP 客户端', link: '/zh/integrations/mcp-clients' },
              { text: '被 webhook 唤醒的 Agent', link: '/zh/integrations/webhooks' },
              { text: 'GitHub Actions', link: '/zh/integrations/github-actions' },
              { text: '无头 Agent 与 CI', link: '/zh/integrations/headless' },
              { text: '配置飞书应用', link: '/zh/integrations/feishu' },
              { text: '部署与 Flux', link: '/zh/integrations/change-events' },
              { text: '值班提示词', link: '/zh/integrations/on-call-prompt' },
            ],
          },
          {
            text: '自托管',
            items: [
              { text: '部署形态', link: '/zh/self-hosting/' },
              { text: '装在服务器上', link: '/zh/self-hosting/install' },
              { text: '检测点位置', link: '/zh/self-hosting/probes' },
              { text: '配置', link: '/zh/self-hosting/configuration' },
              { text: '升级', link: '/zh/self-hosting/upgrade' },
              { text: 'PostgreSQL', link: '/zh/self-hosting/postgres' },
              { text: '备份与恢复', link: '/zh/self-hosting/backup' },
              { text: '安全模型', link: '/zh/self-hosting/security' },
              { text: '常见问题', link: '/zh/self-hosting/troubleshooting' },
            ],
          },
          {
            text: '参考',
            items: [
              { text: '命令行', link: '/zh/reference/cli' },
              { text: 'config.yaml', link: '/zh/reference/config' },
              { text: '目录文件格式', link: '/zh/reference/catalogue' },
              { text: 'REST API', link: '/zh/reference/api' },
              { text: 'MCP 工具', link: '/zh/reference/mcp' },
              { text: 'Webhook', link: '/zh/reference/webhooks' },
              { text: '角色、预设与权限', link: '/zh/reference/permissions' },
              { text: '指标与告警规则', link: '/zh/reference/metrics' },
              { text: '数据来源', link: '/zh/reference/sources' },
            ],
          },
          {
            text: '关于',
            items: [
              { text: '目标', link: '/zh/about/goals' },
              { text: '架构', link: '/zh/about/architecture' },
              { text: '更新日志', link: '/zh/changelog' },
            ],
          },
        ],
      },
    },
  },
};

/**
 * The theme knows the brands of its own release. Until that release has Beacon's, the site wears
 * the OpenLinkOS brand; it takes Beacon's by itself once the theme is updated.
 */
const define = (config: Omit<OpenLinkOSConfig, 'brand'>) => {
  try {
    return defineOpenLinkOSConfig({ ...config, brand: 'beacon' });
  } catch (error) {
    if (!String(error).includes('unknown brand "beacon"')) throw error;
    return defineOpenLinkOSConfig({ ...config, brand: 'openlinkos' });
  }
};

export default define(site);
