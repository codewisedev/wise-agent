# syntax=docker/dockerfile:1
ARG NODE_VERSION=26-alpine3.23
ARG NGINX_VERSION=1.29-alpine

FROM node:${NODE_VERSION} AS build
ENV PNPM_HOME=/pnpm PATH=/pnpm:$PATH
RUN npm install -g pnpm@12.16.1
WORKDIR /src
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY apps/dashboard/package.json apps/dashboard/
RUN --mount=type=cache,target=/pnpm/store pnpm install --frozen-lockfile --filter @wise/dashboard...
COPY apps/dashboard apps/dashboard
COPY packages/contracts/api packages/contracts/api
RUN pnpm -C apps/dashboard build

FROM nginxinc/nginx-unprivileged:${NGINX_VERSION}
COPY apps/dashboard/nginx.conf /etc/nginx/conf.d/default.conf
COPY apps/dashboard/security-headers.conf /etc/nginx/snippets/security-headers.conf
COPY --from=build /src/apps/dashboard/dist /usr/share/nginx/html
EXPOSE 8080
