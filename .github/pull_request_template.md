## What and why

<!-- Which goal in docs/goals.md this serves, what it changes, and the problem it solves. Link related issues. -->

## Checklist

- [ ] `go test ./...` and `golangci-lint run ./...` (again with `GOOS=linux`) pass.
- [ ] A new operation is both a REST route and an MCP tool, through the same method, with a scope check, an audit entry and an echo to the alert chat, and is listed in `internal/api/openapi.yaml` and in `docs/reference/` (both languages).
- [ ] User-facing changes are in `CHANGELOG.md` under Unreleased, and in the docs: the English page and its Chinese page under `docs/zh/` with its `sourceHash`, so that `pnpm build` in `docs/` passes; or this description asks a maintainer to add the translation.
- [ ] Nothing internal: no secrets, internal host names, real addresses or deployment state; examples use `example.com`.
