# Wise toolchain image: Python + git.
ARG PYTHON_IMAGE=python:3.13-alpine3.23
FROM ${PYTHON_IMAGE}
ARG APK_MIRROR=
RUN if [ -n "$APK_MIRROR" ]; then sed -i "s#https\?://dl-cdn.alpinelinux.org/alpine#${APK_MIRROR}#" /etc/apk/repositories; fi \
 && apk add --no-cache git
LABEL wise.toolchain="python"
