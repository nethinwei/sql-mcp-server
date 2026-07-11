# syntax=docker/dockerfile:1

FROM golang:1.26.5-alpine AS build

ARG VERSION=dev
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X github.com/nethinwei/sql-mcp-server/version.value=${VERSION}" \
    -o /out/sql-mcp-server ./cmd/sql-mcp-server

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/sql-mcp-server /usr/local/bin/sql-mcp-server

USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/sql-mcp-server"]
