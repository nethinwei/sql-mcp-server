# syntax=docker/dockerfile:1

# The admin console (web/admin) is embedded into the binary with go:embed.
# Both build stages run on the build platform: the console is the same
# static files for every platform and Go cross-compiles, so a multi-arch
# build emulates nothing (Node under QEMU has crashed release builds).
FROM --platform=$BUILDPLATFORM node:24-alpine AS web

WORKDIR /src/web/admin
RUN corepack enable
COPY web/admin/package.json web/admin/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/admin ./
# Inputs outside web/admin: GraphQL codegen, the merge rules, and the config schema and shared
# validation cases read by the type-checked tests.
COPY x/admin/graph/schema.graphqls x/admin/graph/planning.graphqls /src/x/admin/graph/
COPY x/admin/configapp/testdata/merge_schema.json /src/x/admin/configapp/testdata/
COPY core/config/schema.json /src/core/config/
COPY core/config/testdata/rules.json core/config/testdata/entity_refs.json /src/core/config/testdata/
RUN pnpm build

FROM --platform=$BUILDPLATFORM golang:1.26.9-alpine AS build

ARG VERSION=dev
ARG TARGETOS TARGETARCH
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
COPY --from=web /src/x/admin/ui/dist/app x/admin/ui/dist/app
# The config store's default location; owned by nonroot so a named volume
# mounted there starts out writable.
RUN mkdir -p /out/data
RUN test -f x/admin/ui/dist/app/index.html && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags="-s -w -X github.com/nethinwei/sql-mcp-server/version.value=${VERSION}" \
    -o /out/sql-mcp-server ./cmd/sql-mcp-server

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/sql-mcp-server /usr/local/bin/sql-mcp-server
COPY --from=build --chown=65532:65532 /out/data /var/lib/sql-mcp-server

LABEL io.modelcontextprotocol.server.name="io.github.nethinwei/sql-mcp-server"

USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/sql-mcp-server"]
