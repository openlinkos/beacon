// Keeps the reference pages in step with the code: every REST operation in internal/api/openapi.yaml,
// every gateway route, every error code the API and the gateway answer with, every token scope and every
// MCP tool must appear, in backticks, on the English and the Chinese page that lists them. `vitepress
// build` fails otherwise, naming what is missing.

import { readdirSync, readFileSync } from 'node:fs';

const root = new URL('../../', import.meta.url);
const read = (path: string): string => readFileSync(new URL(path, root), 'utf8');
const goFiles = (dir: string): string[] =>
  readdirSync(new URL(dir, root))
    .filter((f) => f.endsWith('.go') && !f.endsWith('_test.go'))
    .map((f) => read(`${dir}/${f}`));

/** `METHOD /api/v1/path` for every operation of the OpenAPI document. */
const operations = (): string[] => {
  const spec = read('internal/api/openapi.yaml');
  const base = /^servers:\s*\r?\n\s+- url: (\S+)/m.exec(spec)?.[1] ?? '';
  const lines = spec.split(/\r?\n/);
  const ops: string[] = [];
  let inPaths = false;
  let path = '';
  for (const line of lines) {
    if (/^paths:\s*$/.test(line)) {
      inPaths = true;
      continue;
    }
    if (!inPaths) continue;
    if (/^\S/.test(line)) break;
    const p = /^ {2}(\/\S*):\s*$/.exec(line);
    if (p) path = p[1];
    const m = /^ {4}(get|put|post|delete|patch):\s*$/.exec(line);
    if (m && path) ops.push(`${m[1].toUpperCase()} ${base}${path}`);
  }
  return ops;
};

/** The gateway's routes, as its router registers them. */
const gatewayRoutes = (): string[] =>
  goFiles('internal/gateway').flatMap((src) =>
    [...src.matchAll(/mux\.Handle(?:Func)?\("((?:GET|PUT|POST|DELETE|PATCH) \/[^"]*)"/g)].map(
      (m) => m[1],
    ),
  );

/** The error codes of the OpenAPI document and of the API's and the gateway's handlers. */
const errorCodes = (): string[] => {
  const spec = read('internal/api/openapi.yaml');
  const listed = /code:\s*\n\s*type: string\s*\n\s*enum: \[([^\]]*)\]/.exec(spec)?.[1] ?? '';
  const handlers = [...goFiles('internal/api'), ...goFiles('internal/gateway')].flatMap((src) =>
    [...src.matchAll(/(?:writeError|fail)\(\w+, http\.Status\w+, "([a-z_.]+)"/g)].map((m) => m[1]),
  );
  return [...listed.split(','), ...handlers].map((c) => c.trim()).filter(Boolean);
};

/** The token scopes. */
const scopes = (): string[] => {
  const list = /var Scopes = \[\]string\{([^}]*)\}/.exec(read('internal/store/store.go'))?.[1] ?? '';
  return [...list.matchAll(/"([^"]+)"/g)].map((m) => m[1]);
};

/** The MCP server's tools. */
const tools = (): string[] =>
  goFiles('internal/mcpserver').flatMap((src) =>
    [...src.matchAll(/mcp\.AddTool\(\w+, &mcp\.Tool\{Name: "([a-z_]+)"/g)].map((m) => m[1]),
  );

const unique = (items: string[]): string[] => [...new Set(items)];

/** What the reference pages lack, one line each; empty when they are complete. */
export const checkReference = (): string[] => {
  const problems: string[] = [];
  const expect = (what: string, page: string, items: string[]): void => {
    if (!items.length) {
      problems.push(`found no ${what} in the code; the patterns in docs/.vitepress/reference.ts need updating`);
      return;
    }
    for (const lang of ['', 'zh/']) {
      const file = `docs/${lang}reference/${page}`;
      const text = read(file);
      const missing = unique(items).filter((item) => !text.includes(`\`${item}\``));
      if (missing.length) problems.push(`${file} lacks ${what}: ${missing.join(', ')}`);
    }
  };
  expect('REST operations', 'api.md', operations());
  expect('gateway routes', 'api.md', gatewayRoutes());
  expect('error codes', 'api.md', errorCodes());
  expect('scopes', 'api.md', scopes());
  expect('MCP tools', 'mcp.md', tools());
  return problems;
};
