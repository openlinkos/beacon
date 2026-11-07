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

/**
 * One part of the site: a link in the top nav to its first page, and the sidebar that every page
 * under its directories shows. The sidebar holds that part alone, its landing page and its groups, so
 * that a reader sees where they are and what is next to it, not the whole site at once.
 */
type Part = { text: string; dirs: string[]; sidebar: (Page | Group)[] };

/** The site's parts, in reading order. The top nav and the sidebars are both made from them (below). */
const parts = (zh: boolean): Part[] => {
  const t = (en: string, cn: string) => (zh ? cn : en);
  const p = (link: string, en: string, cn: string): Page => ({ text: t(en, cn), link: `${zh ? '/zh' : ''}${link}` });
  const g = (en: string, cn: string, ...items: Page[]): Group => ({ text: t(en, cn), items });
  return [
    {
      text: t('Guide', '指南'),
      dirs: ['/guide/'],
      sidebar: [
        g(
          'Getting started',
          '入门',
          p('/guide/', 'What is Beacon', 'Beacon 是什么'),
          p('/guide/quickstart', 'Try it on your laptop', '在本机试一试'),
          p('/guide/concepts', 'How Beacon works', 'Beacon 如何工作'),
          p('/guide/using-beacon', 'Using Beacon', '使用 Beacon'),
        ),
        g(
          'Monitoring and alerts',
          '监控与告警',
          p('/guide/monitoring', 'Monitor a service', '检测一个服务'),
          p('/guide/heartbeats', 'Heartbeats and expiring resources', '心跳与到期资源'),
          p('/guide/status-pages', 'Publish a status page', '发布状态页'),
        ),
        g(
          'Agents on call',
          'Agent 值班',
          p('/guide/on-call', 'Put an agent on call', '让 Agent 值班'),
          p('/guide/drills', 'Rehearse an outage', '演练一次故障'),
          p('/guide/console', 'Review what agents did', '复查 Agent 做了什么'),
        ),
        g(
          'Machines and the SSH channel',
          '机器与 SSH 通道',
          p('/guide/machines', 'Give agents a machine', '给 Agent 一台机器'),
          p('/guide/stop', 'Stop an agent, pause everything', '停下 Agent，暂停一切'),
        ),
        g('The team and permissions', '团队与权限', p('/guide/people', 'Invite your team', '邀请你的团队')),
      ],
    },
    {
      text: t('Integrations', '接入'),
      dirs: ['/integrations/'],
      sidebar: [
        p('/integrations/', 'Overview', '概览'),
        g(
          'Agent clients',
          'Agent 客户端',
          p('/integrations/claude-code', 'Claude Code', 'Claude Code'),
          p('/integrations/codex', 'Codex', 'Codex'),
          p('/integrations/cursor', 'Cursor', 'Cursor'),
          p('/integrations/mcp-clients', 'Other MCP clients', '其他 MCP 客户端'),
          p('/integrations/headless', 'Headless agents and CI', '无头 Agent 与 CI'),
          p('/integrations/on-call-prompt', 'The on-call prompt', '值班提示词'),
        ),
        g(
          'Events and notifications',
          '事件与通知',
          p('/integrations/webhooks', 'Agents woken by a webhook', '被 webhook 唤醒的 Agent'),
          p('/integrations/github-actions', 'GitHub Actions', 'GitHub Actions'),
          p('/integrations/change-events', 'Deployments and Flux', '部署与 Flux'),
          p('/integrations/feishu', 'Set up the Feishu app', '配置飞书应用'),
        ),
      ],
    },
    {
      text: t('Self-hosting', '自托管'),
      dirs: ['/self-hosting/'],
      sidebar: [
        g(
          'Install',
          '安装',
          p('/self-hosting/', 'Deployment options', '部署形态'),
          p('/self-hosting/install', 'On a server', '装在服务器上'),
          p('/self-hosting/probes', 'Probe locations', '检测点位置'),
          p('/self-hosting/configuration', 'Configuration', '配置'),
          p('/self-hosting/postgres', 'PostgreSQL', 'PostgreSQL'),
        ),
        g(
          'Keep it running',
          '日常运维',
          p('/self-hosting/upgrade', 'Upgrades', '升级'),
          p('/self-hosting/backup', 'Backup and restore', '备份与恢复'),
          p('/self-hosting/security', 'Security model', '安全模型'),
          p('/self-hosting/troubleshooting', 'Troubleshooting', '常见问题'),
        ),
      ],
    },
    {
      text: t('Reference', '参考'),
      dirs: ['/reference/'],
      sidebar: [
        p('/reference/', 'Overview', '概览'),
        g(
          'Interfaces',
          '接口',
          p('/reference/api', 'REST API', 'REST API'),
          p('/reference/mcp', 'MCP tools', 'MCP 工具'),
          p('/reference/webhooks', 'Webhooks', 'Webhook'),
          p('/reference/cli', 'Command line', '命令行'),
        ),
        g(
          'Files and formats',
          '文件与格式',
          p('/reference/config', 'config.yaml', 'config.yaml'),
          p('/reference/catalogue', 'The catalogue', '目录文件格式'),
          p('/reference/metrics', 'Metrics and alert rules', '指标与告警规则'),
          p('/reference/sources', 'Sources', '数据来源'),
        ),
        g('Access', '权限', p('/reference/permissions', 'Roles, presets and scopes', '角色、预设与权限')),
      ],
    },
    {
      text: t('About', '关于'),
      dirs: ['/about/', '/changelog'],
      sidebar: [
        g(
          'About',
          '关于',
          p('/about/', 'Overview', '概览'),
          p('/about/goals', 'Goals', '目标'),
          p('/about/architecture', 'Architecture', '架构'),
          p('/changelog', 'Changelog', '更新日志'),
        ),
      ],
    },
  ];
};

/** Each part's sidebar, under each of its directories. */
const sidebar = (zh: boolean): DefaultTheme.SidebarMulti =>
  Object.fromEntries(
    parts(zh).flatMap((part) => part.dirs.map((dir) => [`${zh ? '/zh' : ''}${dir}`, part.sidebar])),
  );

/**
 * The top nav: a link to each part, lit on every page of that part. About is a menu instead, the
 * last item: its pages, then the version these docs describe with the changelog and the project's
 * pages on GitHub. Plain links keep the bar within 1280px beside the language and theme switches.
 */
const nav = (zh: boolean): DefaultTheme.NavItem[] => {
  const prefix = zh ? '/zh' : '';
  const first = (item: Page | Group): string => ('items' in item ? item.items[0].link : item.link);
  const lit = (part: Part) => `^${prefix}(${part.dirs.join('|')})`;
  const all = parts(zh);
  const about = all.pop()!;
  const pages = about.sidebar.flatMap((item) => ('items' in item ? item.items : [item]));
  return [
    ...all.map((part) => ({ text: part.text, link: first(part.sidebar[0]), activeMatch: lit(part) })),
    {
      text: about.text,
      activeMatch: lit(about),
      items: [
        { items: pages.filter((page) => !page.link.endsWith('/changelog')) },
        {
          text: version ?? (zh ? '发布页' : 'Releases'),
          items: [
            ...pages.filter((page) => page.link.endsWith('/changelog')),
            { text: zh ? '发布页' : 'Releases', link: `${repo}/releases` },
            { text: zh ? '参与贡献' : 'Contributing', link: `${repo}/blob/main/CONTRIBUTING.md` },
            { text: zh ? '安全政策' : 'Security policy', link: `${repo}/blob/main/SECURITY.md` },
          ],
        },
      ],
    },
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
      themeConfig: { nav: nav(false), sidebar: sidebar(false) },
    },
    zh: {
      description: '为管理服务器的 AI Agent 打造的自托管监控与 SSH 接入。',
      themeConfig: { nav: nav(true), sidebar: sidebar(true) },
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
