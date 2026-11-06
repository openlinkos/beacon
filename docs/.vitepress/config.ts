import { readFileSync } from 'node:fs';
import type { DefaultTheme } from 'vitepress';
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

type Page = { text: string; link: string };
type Group = { text: string; items: Page[] };
type Section = { text: string; items: (Page | Group)[] };

/**
 * The site's sections, in reading order: each starts with its landing page, and a section that
 * covers several things groups its pages under them. The sidebar shows them as they are, and the
 * top nav is made from the same list (below), so the two never disagree.
 */
const sections = (zh: boolean): Section[] => {
  const t = (en: string, cn: string) => (zh ? cn : en);
  const p = (link: string, en: string, cn: string): Page => ({ text: t(en, cn), link: `${zh ? '/zh' : ''}${link}` });
  return [
    {
      text: t('Getting started', '入门'),
      items: [
        p('/guide/', 'What is Beacon', 'Beacon 是什么'),
        p('/guide/quickstart', 'Try it on your laptop', '在本机试一试'),
        p('/guide/concepts', 'How Beacon works', 'Beacon 如何工作'),
      ],
    },
    {
      text: t('Using Beacon', '使用 Beacon'),
      items: [
        p('/guide/using-beacon', 'Overview', '概览'),
        {
          text: t('Monitoring and alerts', '监控与告警'),
          items: [
            p('/guide/monitoring', 'Monitor a service', '检测一个服务'),
            p('/guide/heartbeats', 'Heartbeats and expiring resources', '心跳与到期资源'),
            p('/guide/status-pages', 'Publish a status page', '发布状态页'),
          ],
        },
        {
          text: t('Agents on call', 'Agent 值班'),
          items: [
            p('/guide/on-call', 'Put an agent on call', '让 Agent 值班'),
            p('/guide/drills', 'Rehearse an outage', '演练一次故障'),
            p('/guide/console', 'Review what agents did', '复查 Agent 做了什么'),
          ],
        },
        {
          text: t('Machines and the SSH channel', '机器与 SSH 通道'),
          items: [
            p('/guide/machines', 'Give agents a machine', '给 Agent 一台机器'),
            p('/guide/stop', 'Stop an agent, pause everything', '停下 Agent，暂停一切'),
          ],
        },
        {
          text: t('The team and permissions', '团队与权限'),
          items: [p('/guide/people', 'Invite your team', '邀请你的团队')],
        },
      ],
    },
    {
      text: t('Integrations', '接入'),
      items: [
        p('/integrations/', 'Overview', '概览'),
        {
          text: t('Agent clients', 'Agent 客户端'),
          items: [
            p('/integrations/claude-code', 'Claude Code', 'Claude Code'),
            p('/integrations/codex', 'Codex', 'Codex'),
            p('/integrations/cursor', 'Cursor', 'Cursor'),
            p('/integrations/mcp-clients', 'Other MCP clients', '其他 MCP 客户端'),
            p('/integrations/headless', 'Headless agents and CI', '无头 Agent 与 CI'),
            p('/integrations/on-call-prompt', 'The on-call prompt', '值班提示词'),
          ],
        },
        {
          text: t('Events and notifications', '事件与通知'),
          items: [
            p('/integrations/webhooks', 'Agents woken by a webhook', '被 webhook 唤醒的 Agent'),
            p('/integrations/github-actions', 'GitHub Actions', 'GitHub Actions'),
            p('/integrations/change-events', 'Deployments and Flux', '部署与 Flux'),
            p('/integrations/feishu', 'Set up the Feishu app', '配置飞书应用'),
          ],
        },
      ],
    },
    {
      text: t('Self-hosting', '自托管'),
      items: [
        p('/self-hosting/', 'Deployment options', '部署形态'),
        p('/self-hosting/install', 'On a server', '装在服务器上'),
        p('/self-hosting/probes', 'Probe locations', '检测点位置'),
        p('/self-hosting/configuration', 'Configuration', '配置'),
        p('/self-hosting/upgrade', 'Upgrades', '升级'),
        p('/self-hosting/postgres', 'PostgreSQL', 'PostgreSQL'),
        p('/self-hosting/backup', 'Backup and restore', '备份与恢复'),
        p('/self-hosting/security', 'Security model', '安全模型'),
        p('/self-hosting/troubleshooting', 'Troubleshooting', '常见问题'),
      ],
    },
    {
      text: t('Reference', '参考'),
      items: [
        p('/reference/', 'Overview', '概览'),
        p('/reference/api', 'REST API', 'REST API'),
        p('/reference/mcp', 'MCP tools', 'MCP 工具'),
        p('/reference/webhooks', 'Webhooks', 'Webhook'),
        p('/reference/cli', 'Command line', '命令行'),
        p('/reference/config', 'config.yaml', 'config.yaml'),
        p('/reference/catalogue', 'The catalogue', '目录文件格式'),
        p('/reference/permissions', 'Roles, presets and scopes', '角色、预设与权限'),
        p('/reference/metrics', 'Metrics and alert rules', '指标与告警规则'),
        p('/reference/sources', 'Sources', '数据来源'),
      ],
    },
    {
      text: t('About', '关于'),
      items: [
        p('/about/', 'Overview', '概览'),
        p('/about/goals', 'Goals', '目标'),
        p('/about/architecture', 'Architecture', '架构'),
        p('/changelog', 'Changelog', '更新日志'),
      ],
    },
  ];
};

/**
 * The top nav, made from the sections. Getting started and Using Beacon share one menu, Guide, as
 * its two groups, so that the bar keeps room for the language and theme switches at 1280px; every
 * other section is a menu of its own, holding its pages and groups as the sidebar does. The
 * changelog sits under the version these docs describe, with the project's pages on GitHub.
 */
const nav = (zh: boolean): DefaultTheme.NavItem[] => {
  const [start, use, ...rest] = sections(zh);
  const flat = (section: Section): DefaultTheme.NavItemChildren => ({
    text: section.text,
    items: section.items.flatMap((item) => ('items' in item ? item.items : [item])),
  });
  const item = (page: Page | Group): DefaultTheme.NavItemWithChildren['items'][number] => {
    if ('items' in page || !page.link.endsWith('/changelog')) return page;
    return {
      text: version ?? (zh ? '发布页' : 'Releases'),
      items: [
        page,
        { text: zh ? '发布页' : 'Releases', link: `${repo}/releases` },
        { text: zh ? '参与贡献' : 'Contributing', link: `${repo}/blob/main/CONTRIBUTING.md` },
        { text: zh ? '安全政策' : 'Security policy', link: `${repo}/blob/main/SECURITY.md` },
      ],
    };
  };
  return [
    { text: zh ? '指南' : 'Guide', items: [flat(start), flat(use)] },
    ...rest.map((section) => ({ text: section.text, items: section.items.map(item) })),
  ];
};

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
      themeConfig: { nav: nav(false), sidebar: sections(false) },
    },
    zh: {
      description: '为管理服务器的 AI Agent 打造的自托管监控与 SSH 接入。',
      themeConfig: { nav: nav(true), sidebar: sections(true) },
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
