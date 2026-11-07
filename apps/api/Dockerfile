# syntax=docker/dockerfile:1
ARG GO_VERSION=1.26.5
ARG ALPINE_VERSION=3.23

FROM golang:${GO_VERSION}-alpine${ALPINE_VERSION} AS build
ARG GOPROXY=https://proxy.golang.org,direct
ENV CGO_ENABLED=0 GOPROXY=${GOPROXY} GOFLAGS=-buildvcs=false
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY apps/api ./apps/api
COPY migrations ./migrations
COPY packages/contracts ./packages/contracts
COPY packages/agent-definitions ./packages/agent-definitions
COPY packages/prompts ./packages/prompts
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w" -o /out/wise-api ./apps/api/cmd/wise-api

FROM alpine:${ALPINE_VERSION}
# Claude Code and Codex CLIs power the CLI model providers; they run in the
# wise-cli-runner container (set INSTALL_CLAUDE_CODE=false to skip both).
ARG INSTALL_CLAUDE_CODE=true
ARG CLAUDE_CODE_VERSION=2.1.319
ARG CODEX_VERSION=0.163.0
# Optional Alpine mirror (e.g. https://mirror.arvancloud.ir/alpine) when dl-cdn is unreachable.
ARG APK_MIRROR=
# git is required to inspect registered repositories.
RUN if [ -n "$APK_MIRROR" ]; then sed -i "s#https\?://dl-cdn.alpinelinux.org/alpine#${APK_MIRROR}#" /etc/apk/repositories; fi \
 && apk add --no-cache git ca-certificates tzdata \
 && adduser -D -H -u 10001 wise \
 && if [ "$INSTALL_CLAUDE_CODE" = "true" ]; then \
      apk add --no-cache nodejs npm libstdc++ ripgrep \
      && npm install -g --omit=dev "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" "@openai/codex@${CODEX_VERSION}" \
      && npm cache clean --force; \
    fi
WORKDIR /app
COPY configs ./configs
COPY --from=build /out/wise-api /usr/local/bin/wise-api
USER 10001
EXPOSE 8080
ENTRYPOINT ["wise-api"]
CMD ["serve"]
