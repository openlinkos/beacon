# Contributing

Beacon is self-hosted infrastructure for AI agents, shared with the people who manage them. [docs/about/goals.md](docs/about/goals.md) says what it is for, what it will not do, and how to choose when goals pull apart; [AGENTS.md](AGENTS.md) holds the rules for anyone, person or agent, who changes the code. This page is the short form: how to build and test, what a change must respect, and how it gets in and out.

Before you plan a change, say which goal it serves. A change that moves neither coverage nor closure and holds no guardrail waits.

Everyone taking part is expected to follow the [Code of Conduct](CODE_OF_CONDUCT.md).

## Issues and questions

Report a bug or ask for a feature through the [issue forms](https://github.com/openlinkos/beacon/issues/new/choose); they ask for the version, the part of Beacon and, for a feature, the goal it serves. Questions and ideas still taking shape go to [Discussions](https://github.com/openlinkos/beacon/discussions). Describe problems with `example.com` names and made-up values, never with secrets, real host names or the content of real alerts. Vulnerabilities go to security@linkos.io, not to either: see [SECURITY.md](SECURITY.md).

## Build and test

Go, at the version `go.mod` names; no cgo and no other toolchain (only the [documentation site](#the-documentation-site) needs Node.js). The three binaries are `beacon` (the service), `beacon-probe` (the probe agent) and `beacon-oncall` (the harness that runs an agent when a hook wakes it).

```bash
go build ./cmd/beacon ./cmd/beacon-probe ./cmd/beacon-oncall
go test ./...                                    # CI also runs with -race, and on Windows
golangci-lint run ./...                          # v2.15 (.golangci.yml); and again with
GOOS=linux golangci-lint run ./...               # some files are Unix-only
go run ./cmd/beacon check-config -config config.example.yaml   # needs the secret files it names
```

The reference deployment under `deploy/` has its own checks, which the `deploy` workflow runs: `docker compose -f deploy/compose.yaml config -q`, `caddy validate`, `vmalert -dryRun` and `vmalert-tool unittest` on `deploy/rules`, `check-config` on `deploy/config.yaml` with generated secrets, and `shellcheck` on the scripts.

## The documentation site

`docs/` is the site at [beacon.openlinkos.com](https://beacon.openlinkos.com), built with [VitePress](https://vitepress.dev) on the shared OpenLinkOS theme (`@openlinkos/vitepress-theme`). It needs Node.js 22.16 or later (24 recommended) and pnpm; `corepack enable` gives the version `docs/package.json` names.

```bash
cd docs
pnpm install
pnpm dev                                         # http://localhost:5173, reloads as you edit
pnpm build                                       # what CI runs; fails with the exact fix
pnpm preview                                     # serves the build
```

`pnpm build` fails, saying what to do, unless:

- **every page has its Chinese counterpart**: `docs/x.md` and `docs/zh/x.md`, with the same `##` and `###` headings in the same order and the same code blocks. A Chinese heading carries the English heading's id as an explicit anchor, `## 安装 {#install}`. Ids are GitHub's, so a link to a heading works on GitHub and on the site alike: lower case, punctuation dropped, spaces as dashes (`## The alert's page: /alerts/{id}` is `#the-alerts-page-alertsid`);
- **each Chinese page records the English version it translates** in its frontmatter, `sourceHash: sha256-…`, the first 8 hex digits of the SHA-256 of the English file with LF line endings. When the English page changes, the build prints the line to paste once the translation is updated. To compute it yourself:

  ```bash
  node -e "const c=require('fs').readFileSync(process.argv[1],'utf8').replace(/\r\n/g,'\n');console.log('sha256-'+require('crypto').createHash('sha256').update(c,'utf8').digest('hex').slice(0,8))" docs/guide/on-call.md
  ```

- **links stay within their language**: relative `.md` links between pages, which resolve to the same language; English pages never link into `docs/zh/`, and Chinese pages link only there. Link to files outside `docs/` with full GitHub URLs. There is no "English | 简体中文" line at the top of a page: the site has a language menu;
- **the reference is complete**: `docs/reference/api.md` and `mcp.md`, in both languages, name every REST operation in `internal/api/openapi.yaml`, every gateway route, every error code the API and the gateway answer with, every token scope and every MCP tool (`docs/.vitepress/reference.ts`). A new route or tool is documented in the same pull request;
- no link is dead.

If you cannot write Chinese, change the English page only and say so in the pull request; a maintainer adds the translation before merging. The home page is `docs/index.md` (and `docs/zh/index.md`); the navigation is in `docs/.vitepress/config.ts`.

### Writing a page

People scan a page before they read it, and agents read it whole; both are served by the same things. [What is Beacon](docs/guide/index.md) is the model.

- **Open with what the page gets you**, in one or two sentences (40 words at most). Then a short list of what you need, if anything, before the first step.
- **One idea to a sentence.** Aim for sentences under 20 words. Where a sentence strings clauses together with semicolons and colons, split it, or make it a list.
- **Short paragraphs**: four sentences or fewer, never more than about 80 words. Three or more parallel things are a list or a table, not a paragraph.
- **Steps are numbered**, each opening with what to do in bold, then the command, then how to see that it worked.
- **A how-to page does the task.** Why things work the way they do belongs in [How Beacon works](docs/guide/concepts.md) or [Architecture](docs/about/architecture.md); link to it instead of explaining it on the way.
- **Keep headings stable**: other pages and the Chinese page link to their ids. Rename one only together with every link to it.

## The rules, in short

- **English** in code, comments, logs, CLI output, scripts, commit messages and `docs/`, in British spelling. Chinese lives only in `internal/i18n/zh.go`, `README.zh-CN.md` and `docs/zh/`.
- **Docs come in pairs**: `docs/x.md` and `docs/zh/x.md` (with its `sourceHash`, as [above](#the-documentation-site)), `README.md` and `README.zh-CN.md`, section for section, changed together. User-facing changes go into `CHANGELOG.md` under Unreleased.
- **What people read goes through `internal/i18n`**: card text, toasts and chat messages are English format strings passed to `i18n.T` or `(*Bridge).t`, with the Chinese in `zh.go`. `TestCatalogue` fails on a missing or unused translation, `TestNoChinese` on Chinese anywhere else.
- **Every operation is REST and MCP**, through the same bridge method, with a scope check, an audit entry and an echo to the alert chat. API errors are `{"error", "code"}`; codes are stable and listed in `internal/api/openapi.yaml`.
- **Secrets** come only from `*_file` paths named in the configuration, are never logged and never put in errors.
- **Security first**: tokens are stored hashed, with scopes and an expiry; a machine's token writes only as its own subject. Do not weaken a check to make a test pass.
- **The probe protocol** is shared across versions: add fields and routes, never change or remove them.
- **Probe agents stay small**: about 15 MB resident, 64 MB hard limit. No new dependency in `cmd/beacon-probe` without measuring.
- **Close to how agents work**: SSH, the shell, HTTP with JSON, Prometheus queries, MCP; no protocol or configuration language of our own. **No approval workflows**: Beacon records; the agent's harness decides. **Beacon calls no model.**
- **Public repository, no internal information**: machines are named by place and network, examples use `example.com`, nothing about any particular deployment. Code that only one deployment needs is configuration, kept outside this repository.
- **Ask before adding a dependency**, and keep `go.mod` tidy.

## Commits and pull requests

[Conventional Commits](https://www.conventionalcommits.org) in English: `<type>(<scope>): <summary>`, with the types `feat`, `fix`, `docs`, `refactor`, `test`, `perf`, `build`, `ci` and `chore`. The body says why, when the summary does not. No attribution trailers (`Co-Authored-By` and the like). Run the tests and both lints before every commit.

A pull request does one thing; [the template](.github/pull_request_template.md) has the checklist. CI runs the tests on Linux and Windows, the linter, `govulncheck`, `actionlint` and `zizmor` on the workflows, the builds for `linux/amd64` and `linux/arm64`, a release snapshot, the documentation site's build, and the deployment checks; all of it must pass.

No sign-off (Developer Certificate of Origin) and no contributor licence agreement is asked for: by contributing, you agree that your contribution is licensed under the [Apache License 2.0](LICENSE), as its section 5 says.

## Releases

A release starts as a release candidate: tag `vX.Y.Z-rc.N`. The `release` workflow builds the binaries and archives for `linux/amd64` and `linux/arm64` (reproducibly), signs `SHA256SUMS` and the image `ghcr.io/openlinkos/beacon` with cosign (keyless), attaches SBOMs and build provenance, publishes the GitHub release, then pulls the image anonymously, verifies its signature and runs it once. A pre-release gets only its own image tag. Run the candidate somewhere real; the makers run it in production. When it holds, tag `vX.Y.Z` on the same commit: the release also moves the `vX.Y` and `latest` tags.

Before tagging, move the `CHANGELOG.md` entries from Unreleased to the version, and raise the pinned version in `deploy/.env.example` and `deploy/probe/install.sh`.

The site is not tied to releases: the `site` workflow builds it and publishes it on Cloudflare Pages whenever `docs/` or `CHANGELOG.md` changes on `main`. A pull request that touches them, from a branch of this repository, gets a preview of the site at its own address, linked in a comment on the pull request; every deployment, preview or not, passes the smoke test (`docs/smoke.sh`) first. Moving a page means adding its old address to `docs/public/_redirects`. The version in the site's navigation is the newest one in `CHANGELOG.md`.

## Security

Vulnerabilities go to security@linkos.io, not to the issue tracker: see [SECURITY.md](SECURITY.md).
