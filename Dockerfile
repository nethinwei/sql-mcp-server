# syntax=docker/dockerfile:1

# The admin console (web/admin) is embedded into the binary with go:embed.
FROM node:24-alpine AS web

WORKDIR /src/web/admin
RUN corepack enable
COPY web/admin/package.json web/admin/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/admin ./
# Inputs outside web/admin: GraphQL codegen and the config schema used by
# the type-checked tests.
COPY x/admin/graph/schema.graphqls /src/x/admin/graph/
COPY core/config/schema.json /src/core/config/
RUN pnpm build

FROM golang:1.26.8-alpine AS build

ARG VERSION=dev
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
COPY --from=web /src/x/admin/ui/dist/app x/admin/ui/dist/app
RUN test -f x/admin/ui/dist/app/index.html && CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X github.com/nethinwei/sql-mcp-server/version.value=${VERSION}" \
    -o /out/sql-mcp-server ./cmd/sql-mcp-server

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/sql-mcp-server /usr/local/bin/sql-mcp-server

LABEL io.modelcontextprotocol.server.name="io.github.nethinwei/sql-mcp-server"

USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/sql-mcp-server"]
