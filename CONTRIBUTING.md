# Contributing

Beacon is infrastructure for AI agents that run operations. [docs/goals.md](docs/goals.md) says what it is for, what it will not do, and how to choose when goals pull apart; [AGENTS.md](AGENTS.md) holds the rules for anyone, person or agent, who changes the code. This page is the short form: how to build and test, what a change must respect, and how it gets in and out.

Before you plan a change, say which goal it serves. A change that moves neither coverage nor closure and holds no guardrail waits.

## Build and test

Go, at the version `go.mod` names; no cgo and no other toolchain. The two binaries are `linkos-beacon` (the service) and `linkos-probe` (the probe agent).

```bash
go build ./cmd/linkos-beacon ./cmd/linkos-probe
go test ./...                                    # CI also runs with -race, and on Windows
golangci-lint run ./...                          # v2.15 (.golangci.yml); and again with
GOOS=linux golangci-lint run ./...               # some files are Unix-only
go run ./cmd/linkos-beacon check-config -config config.example.yaml   # needs the secret files it names
```

The reference deployment under `deploy/` has its own checks, which the `deploy` workflow runs: `docker compose -f deploy/compose.yaml config -q`, `caddy validate`, `vmalert -dryRun` and `vmalert-tool unittest` on `deploy/rules`, `check-config` on `deploy/config.yaml` with generated secrets, and `shellcheck` on the scripts.

## The rules, in short

- **English** in code, comments, logs, CLI output, scripts, commit messages and `docs/`, in British spelling. Chinese lives only in `internal/i18n/zh.go`, `README.zh-CN.md` and `docs/zh/`.
- **Docs come in pairs**: `docs/x.md` and `docs/zh/x.md`, `README.md` and `README.zh-CN.md`, section for section, changed together. User-facing changes go into `CHANGELOG.md` under Unreleased.
- **What people read goes through `internal/i18n`**: card text, toasts and chat messages are English format strings passed to `i18n.T` or `(*Bridge).t`, with the Chinese in `zh.go`. `TestCatalogue` fails on a missing or unused translation, `TestNoChinese` on Chinese anywhere else.
- **Every operation is REST and MCP**, through the same bridge method, with a scope check, an audit entry and an echo to the alert chat. API errors are `{"error", "code"}`; codes are stable and listed in `internal/api/openapi.yaml`.
- **Secrets** come only from `*_file` paths named in the configuration, are never logged and never put in errors.
- **Security first**: tokens are stored hashed, with scopes and an expiry; a machine's token writes only as its own subject. Do not weaken a check to make a test pass.
- **The probe protocol** is shared across versions: add fields and routes, never change or remove them.
- **Probe agents stay small**: about 15 MB resident, 64 MB hard limit. No new dependency in `cmd/linkos-probe` without measuring.
- **Close to how agents work**: SSH, the shell, HTTP with JSON, Prometheus queries, MCP; no protocol or configuration language of our own. **No approval workflows**: Beacon records; the agent's harness decides. **Beacon calls no model.**
- **Public repository, no internal information**: machines are named by place and network, examples use `example.com`, nothing about any particular deployment. Code that only one deployment needs is configuration, kept outside this repository.
- **Ask before adding a dependency**, and keep `go.mod` tidy.

## Commits and pull requests

[Conventional Commits](https://www.conventionalcommits.org) in English: `<type>(<scope>): <summary>`, with the types `feat`, `fix`, `docs`, `refactor`, `test`, `perf`, `build`, `ci` and `chore`. The body says why, when the summary does not. No attribution trailers (`Co-Authored-By` and the like). Run the tests and both lints before every commit.

A pull request does one thing. CI runs the tests on Linux and Windows, the linter, `govulncheck`, `actionlint` and `zizmor` on the workflows, the builds for `linux/amd64` and `linux/arm64`, a release snapshot, and the deployment checks; all of it must pass.

## Releases

A release starts as a release candidate: tag `vX.Y.Z-rc.N`. The `release` workflow builds the binaries and archives for `linux/amd64` and `linux/arm64` (reproducibly), signs `SHA256SUMS` and the image `ghcr.io/openlinkos/beacon` with cosign (keyless), attaches SBOMs and build provenance, publishes the GitHub release, then pulls the image anonymously, verifies its signature and runs it once. A pre-release gets only its own image tag. Run the candidate somewhere real; the makers run it in production. When it holds, tag `vX.Y.Z` on the same commit: the release also moves the `vX.Y` and `latest` tags.

Before tagging, move the `CHANGELOG.md` entries from Unreleased to the version, and raise the pinned version in `deploy/.env.example` and `deploy/probe/install.sh`.

## Security

Vulnerabilities go to security@linkos.io, not to the issue tracker: see [SECURITY.md](SECURITY.md).
