# AGENTS.md

Instructions for AI coding agents working in this repository. `CLAUDE.md` is a symlink to this file; edit `AGENTS.md` only.

## Project

Beacon · 烽火 (`linkos-beacon`, and the probe agent `linkos-probe`) tells whether services work for their users, probed from where the users are, and closes the loop with the people on call and their AI agents: a gateway for machines (enrolled tokens, labels set by the server, targets handed out), alert routing in place of Alertmanager (grouping, inhibition, silences), Feishu app cards they can acknowledge or silence from, escalation to their phones through Pushover, a dead man's switch on healthchecks.io, and the same operations through a REST API and a built-in MCP server. Public status pages follow. Go; one service and one probe binary; metrics and rules live in VictoriaMetrics and vmalert. LinkOS runs its own status service (LinkOS Status) on Beacon, configured in the ops repository.

## Commands

```bash
go test ./...                  # unit tests; CI also runs -race and Windows
golangci-lint run ./...        # also with GOOS=linux: some files are Unix-only
go run ./cmd/linkos-beacon check-config -config config.example.yaml   # needs the secret files it names
```

## Rules

- **Use English** for code, comments, logs, CLI output, scripts and commit messages. British spelling, as in the docs.
- **What people read goes through `internal/i18n`**: card text, toasts and chat messages are English format strings passed to `i18n.T` or `(*Bridge).t`, with the Chinese in `internal/i18n/zh.go`, keyed by that string. `TestCatalogue` fails on a missing or unused translation, and `TestNoChinese` on Chinese anywhere outside the catalogue and `docs/zh/`. Logs, CLI output and API errors stay English.
- **API errors** are `{"error", "code"}`; codes are stable and listed in `internal/api/openapi.yaml`, which `TestOpenAPIInStep` keeps in step with the routes. Every new operation is offered both as a REST route and as an MCP tool, through the same bridge method, with a scope check, an audit entry and an echo to the alert chat.
- **Secrets** are only ever read from `*_file` paths named in the configuration; never log them, never put them in errors.
- **Security first**: tokens are stored as SHA-256 only; `/metrics` answers direct connections only; the webhook needs its bearer token; only the on-call people may act on cards. Do not weaken a check to make a test pass.
- **Public repository**: name machines by location and network, never by internal host names, vendors or real addresses.
- **Ask before adding dependencies**; keep `go.mod` tidy.
- **Docs**: English in `README.md` and `docs/`, Chinese in `README.zh-CN.md` and `docs/zh/`, section for section. Change both together.

## Git

- Conventional Commits in English: `<type>(<scope>): <summary>`, types `feat | fix | docs | refactor | test | perf | build | ci | chore`.
- Run the tests and the linter before committing. Do not add attribution trailers such as `Co-Authored-By`.
- Releases: tag `vX.Y.Z-rc.N` first (the release workflow builds, signs and verifies), test it, then tag `vX.Y.Z`.
