# Wise toolchain image: Go + git.
ARG GO_IMAGE=golang:1.26.5-alpine3.23
FROM ${GO_IMAGE}
ARG APK_MIRROR=
RUN if [ -n "$APK_MIRROR" ]; then sed -i "s#https\?://dl-cdn.alpinelinux.org/alpine#${APK_MIRROR}#" /etc/apk/repositories; fi \
 && apk add --no-cache git build-base
LABEL wise.toolchain="go"
