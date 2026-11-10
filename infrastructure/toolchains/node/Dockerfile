# Wise toolchain image: Node.js + git (sandboxes need git for diffs and commits),
# bash and ripgrep (coding CLIs need a shell and rg for their Bash, Grep and Glob tools).
ARG NODE_IMAGE=node:22-alpine3.23
FROM ${NODE_IMAGE}
ARG APK_MIRROR=
RUN if [ -n "$APK_MIRROR" ]; then sed -i "s#https\?://dl-cdn.alpinelinux.org/alpine#${APK_MIRROR}#" /etc/apk/repositories; fi \
 && apk add --no-cache git bash ripgrep
LABEL wise.toolchain="node"
