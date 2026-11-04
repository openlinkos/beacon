// Keeps the reference pages in step with the code: every REST operation in internal/api/openapi.yaml,
// every gateway route, every error code the API and the gateway answer with, every token scope, every
// MCP tool, every command and flag the three binaries take, and every key config.yaml accepts must
// appear, in backticks, on the English and the Chinese page that lists them. `vitepress build` fails
// otherwise, naming what is missing.

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

/** The commands a binary's `switch args[0]` (or `switch verb`) takes: the commands of `beacon`,
 * `beacon-probe` and `beacon-oncall`, and the subcommands of the `beacon` groups that switch over
 * their first word. */
const commands = (dir: string): string[] =>
  goFiles(dir).flatMap((src) =>
    [...src.matchAll(/switch (?:args\[0\]|verb) \{([\s\S]*?)\n\t\}/g)].flatMap((m) =>
      [...m[1].matchAll(/^[ \t]*case (.*):$/gm)].flatMap((c) =>
        [...c[1].matchAll(/"([a-z][a-z-]*)"/g)].map((w) => w[1]),
      ),
    ),
  );

/** The subcommands of `beacon config`, which its switch matches with == rather than case. */
const configSubcommands = (): string[] =>
  [...read('cmd/beacon/config.go').matchAll(/args\[0\] == "([a-z-]+)"/g)].map((m) => m[1]);

/** The verbs of `beacon ssh`, which its guard compares one by one. */
const sshVerbs = (): string[] =>
  [...read('cmd/beacon/ssh.go').matchAll(/args\[0\] != "([a-z]+)"/g)].map((m) => m[1]);

/** The kinds `beacon enroll` makes a token for, from the scope each kind is given. */
const enrollKinds = (): string[] => {
  const list = /EnrollKinds = map\[string\]string\{([^}]*)\}/.exec(read('internal/ctl/ctl.go'))?.[1] ?? '';
  return [...list.matchAll(/"([a-z]+)":/g)].map((m) => m[1]);
};

/** Every flag a command defines, from its flag set, with the dash it is written with. */
const flags = (dir: string): string[] =>
  goFiles(dir).flatMap((src) =>
    [...src.matchAll(/\bfs\.(?:String|Bool|Int|Int64|Duration|Float64)\("([a-z0-9-]+)"/g)].map(
      (m) => `-${m[1]}`,
    ),
  );

/** The yaml tags of internal/config's structs, by type name: a field is {tag, type} or {tag, fields}
 * for a struct written inline. */
const configStructs = (): Map<string, { tag: string | null; type?: string; fields?: unknown[] }[]> => {
  const structs = new Map<string, { tag: string | null; type?: string; fields?: unknown[] }[]>();
  for (const src of goFiles('internal/config')) {
    for (const m of src.matchAll(/^type (\w+) struct \{([\s\S]*?)\n\}/gm)) {
      const fields: { tag: string | null; type?: string; fields?: unknown[] }[] = [];
      const stack: typeof fields[] = [fields];
      for (const line of m[2].split('\n')) {
        const l = line.replace(/\/\/.*$/, '').trim();
        const open = /^(\w+)\s+struct \{$/.exec(l); // gofmt aligns the name and the keyword
        const close = /^\} `yaml:"([^"]+)"`$/.exec(l);
        const field = /^(\w+)\s+(\S+)\s+`yaml:"([^"]+)"`/.exec(l);
        if (open) {
          const nested: typeof fields = [];
          stack[stack.length - 1].push({ tag: null, fields: nested });
          stack.push(nested);
        } else if (close) {
          stack.pop();
          const outer = stack[stack.length - 1];
          if (outer.length) outer[outer.length - 1].tag = close[1];
        } else if (field) {
          stack[stack.length - 1].push({ tag: field[3], type: field[2] });
        }
      }
      structs.set(m[1], fields);
    }
  }
  return structs;
};

/** Every key config.yaml takes, as dotted paths: the loader refuses unknown keys, so the yaml tags
 * are exactly the keys. A map's keys (escalation, by severity) are expanded from the severities the
 * bridge knows. */
const configKeys = (): string[] => {
  const structs = configStructs();
  const severities = [
    ...(read('internal/config/config.go').match(/var Severities = \[\]string\{([^}]*)\}/)?.[1] ?? '')
      .matchAll(/"([a-z]+)"/g),
  ].map((m) => m[1]);
  const out: string[] = [];
  const walk = (fields: { tag: string | null; type?: string; fields?: unknown[] }[], prefix: string) => {
    for (const f of fields) {
      if (!f.tag || f.tag === '-') continue;
      const path = prefix + f.tag;
      if (f.fields) {
        walk(f.fields as typeof fields, `${path}.`);
        continue;
      }
      const t = (f.type ?? '').replace(/^\*/, '').replace(/^\[\]/, '').replace(/^map\[\]?\w*\]/, ''); // pointers and slices name a struct the same
      if (structs.has(t)) {
        if (f.type?.startsWith('map[')) for (const s of severities) walk(structs.get(t)!, `${path}.${s}.`);
        else walk(structs.get(t)!, `${path}.`);
      } else out.push(path);
    }
  };
  const config = structs.get('Config');
  if (config) walk(config, '');
  return out;
};

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
  expect('beacon commands', 'cli.md', [...commands('cmd/beacon'), ...configSubcommands(), ...sshVerbs(), ...enrollKinds()]);
  expect('beacon flags', 'cli.md', flags('cmd/beacon'));
  expect('beacon-probe commands', 'cli.md', commands('cmd/beacon-probe'));
  expect('beacon-probe flags', 'cli.md', flags('cmd/beacon-probe'));
  expect('beacon-oncall commands', 'cli.md', commands('cmd/beacon-oncall'));
  expect('beacon-oncall flags', 'cli.md', flags('cmd/beacon-oncall'));
  expect('config.yaml keys', 'config.md', configKeys());
  return problems;
};
