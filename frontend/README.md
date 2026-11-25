# Beacon workspace

The React workspace owns the browser routes and renders data from Beacon's same-origin
`/console/api/v1` interface. It uses the existing Beacon sign-in and account permissions.
The API sends no bearer credential to the browser. An incident mutation requires the
session's CSRF token and any incident or acknowledgement version needed by the bridge.

## Local development

Start a local Beacon server, then run:

```sh
pnpm --dir frontend install --frozen-lockfile
BEACON_API_ORIGIN=http://127.0.0.1:17001 pnpm --dir frontend dev
```

Open `http://localhost:5173/workspace/`. Vite proxies sign-in, API requests and the static
replay assets. Use HTTPS for browsers that require it for secure localhost cookies. Use
the supported Node version from `package.json`.

## Packaging

From the repository root:

```sh
sh scripts/build-workspace.sh
go build ./cmd/beacon
```

The release hook builds the frontend and copies only its static output into the Go embed
tree before GoReleaser compiles `beacon`. The image packages that binary and `/console`
opens the React console by default. The frontend, session API and public status pages share
one process and origin. Existing signed detail URLs keep their server-rendered views.
A Go development build without the embedded assets still serves the JSON API for Vite.
