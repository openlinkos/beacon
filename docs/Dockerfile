# The site image (beacon.openlinkos.com): built with VitePress, served by unprivileged nginx on port 8080.
#
# Build from the repository root, which the site reads: the changelog page includes CHANGELOG.md, and the
# reference pages are checked against the API's, the gateway's and the MCP server's sources.
#   docker build -f docs/Dockerfile -t beacon-site .
# Dockerfile.dockerignore beside this file lets only those into the build context. The site is the same files
# on every platform, so it is built once, on the builder's own platform, and an image for another platform needs
# no emulation. The base images are pinned by digest and updated by Dependabot.
FROM --platform=$BUILDPLATFORM docker.io/library/node:26.13.0-alpine3.24@sha256:c6ad9e9c1d80f6eb607c26b4b0a132ff7a2344fad1c78ea3baa2a7d4c8de6d94 AS build
ENV COREPACK_ENABLE_DOWNLOAD_PROMPT=0
WORKDIR /src/docs
RUN corepack enable
COPY docs/package.json docs/pnpm-lock.yaml docs/pnpm-workspace.yaml docs/.npmrc ./
RUN pnpm install --frozen-lockfile
COPY CHANGELOG.md /src/
COPY internal/api/ /src/internal/api/
COPY internal/gateway/ /src/internal/gateway/
COPY internal/mcpserver/ /src/internal/mcpserver/
COPY internal/store/store.go /src/internal/store/
COPY docs/ /src/docs/
RUN pnpm build

FROM docker.io/nginxinc/nginx-unprivileged:1.31.7-alpine@sha256:b5047b9b76845eac124358373c6852377c52e4b05834a47647e955dd91cb8f1e
COPY docs/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /src/docs/.vitepress/dist /usr/share/nginx/html
EXPOSE 8080
