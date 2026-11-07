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
COPY packages/contracts/api ./packages/contracts/api
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w" -o /out/wise-api ./apps/api/cmd/wise-api

FROM alpine:${ALPINE_VERSION}
# git is required to inspect registered repositories.
RUN apk add --no-cache git ca-certificates tzdata \
 && adduser -D -H -u 10001 wise
COPY --from=build /out/wise-api /usr/local/bin/wise-api
USER 10001
EXPOSE 8080
ENTRYPOINT ["wise-api"]
CMD ["serve"]
