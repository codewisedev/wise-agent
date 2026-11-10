# Coding CLIs for task runners (Phase 5b). The binaries are standalone (musl):
# the executor mounts them read-only into runner containers built from the
# repository's own toolchain image, so there is no image per toolchain.
ARG NODE_IMAGE=node:22-alpine3.23
FROM ${NODE_IMAGE} AS fetch
ARG CLAUDE_CODE_VERSION=2.1.319
ARG CODEX_VERSION=0.163.0
ARG APK_MIRROR=
RUN npm install -g --omit=dev "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" "@openai/codex@${CODEX_VERSION}" \
 && R=$(npm root -g) \
 && mkdir -p "/clis/claude-code/${CLAUDE_CODE_VERSION}/musl" "/clis/codex/${CODEX_VERSION}/musl" \
 && cp "$R/@anthropic-ai/claude-code/bin/claude.exe" "/clis/claude-code/${CLAUDE_CODE_VERSION}/musl/claude" \
 && cp "$R/@openai/codex/node_modules/@openai/codex-linux-x64/vendor/x86_64-unknown-linux-musl/bin/codex" "/clis/codex/${CODEX_VERSION}/musl/codex" \
 && chmod 0555 /clis/*/*/musl/* \
 && "/clis/claude-code/${CLAUDE_CODE_VERSION}/musl/claude" --version \
 && "/clis/codex/${CODEX_VERSION}/musl/codex" --version

FROM alpine:3.23
COPY --from=fetch /clis /clis
LABEL wise.coding-clis="true"
