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
          { text: 'Guide', link: '/install' },
          {
            text: 'Reference',
            items: [
              { text: 'REST API', link: '/reference/api' },
              { text: 'MCP tools', link: '/reference/mcp' },
              { text: 'Sources', link: '/sources' },
              { text: 'Architecture', link: '/architecture' },
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
              { text: 'Up in 30 minutes', link: '/install' },
              { text: 'Agents and alerts', link: '/agents' },
              { text: 'The SSH channel', link: '/ssh' },
              { text: 'Catalogue, targets and modules', link: '/catalog' },
              { text: 'The console', link: '/pages' },
              { text: 'Status pages', link: '/status' },
              { text: 'Setting up the Feishu app', link: '/feishu' },
            ],
          },
          {
            text: 'Design',
            items: [
              { text: 'Goals', link: '/goals' },
              { text: 'Architecture', link: '/architecture' },
            ],
          },
          {
            text: 'Reference',
            items: [
              { text: 'REST API', link: '/reference/api' },
              { text: 'MCP tools', link: '/reference/mcp' },
              { text: 'Sources', link: '/sources' },
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
          { text: '指南', link: '/zh/install' },
          {
            text: '参考',
            items: [
              { text: 'REST API', link: '/zh/reference/api' },
              { text: 'MCP 工具', link: '/zh/reference/mcp' },
              { text: '数据来源', link: '/zh/sources' },
              { text: '架构', link: '/zh/architecture' },
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
              { text: '30 分钟上线', link: '/zh/install' },
              { text: 'Agent 与告警', link: '/zh/agents' },
              { text: 'SSH 通道', link: '/zh/ssh' },
              { text: '目录、检测目标和检测模块', link: '/zh/catalog' },
              { text: '控制台', link: '/zh/pages' },
              { text: '状态页', link: '/zh/status' },
              { text: '配置飞书应用', link: '/zh/feishu' },
            ],
          },
          {
            text: '设计',
            items: [
              { text: '目标', link: '/zh/goals' },
              { text: '架构', link: '/zh/architecture' },
            ],
          },
          {
            text: '参考',
            items: [
              { text: 'REST API', link: '/zh/reference/api' },
              { text: 'MCP 工具', link: '/zh/reference/mcp' },
              { text: '数据来源', link: '/zh/sources' },
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
