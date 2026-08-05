# syntax=docker/dockerfile:1
#
# Build:  docker build -t demo-frontend:local frontend/
# (self-contained: build context is this directory, no submodule/workspace
# dependency -- unlike backend/Dockerfile this does not need go.work or any
# third_party subtree.)
#
# VITE_BACKEND_URL / VITE_OTLP_ENDPOINT / VITE_GRAFANA_BASE_URL are baked
# into the static bundle at build time (Vite convention). Override with
# --build-arg to point a built image at a different backend/collector/
# Grafana host without rebuilding from source.

FROM node:22-alpine AS build
WORKDIR /app

ARG VITE_BACKEND_URL=http://localhost:8080
ARG VITE_OTLP_ENDPOINT=http://localhost:4318
ARG VITE_GRAFANA_BASE_URL=http://localhost:3000
ENV VITE_BACKEND_URL=${VITE_BACKEND_URL} \
    VITE_OTLP_ENDPOINT=${VITE_OTLP_ENDPOINT} \
    VITE_GRAFANA_BASE_URL=${VITE_GRAFANA_BASE_URL}

RUN corepack enable

# Install deps first so dependency layers cache independently of source
# changes. NOTE: this image's build context is frontend/ only (self-
# contained, per the demo's Dockerfile contract), so the pnpm workspace's
# root-level pnpm-lock.yaml is intentionally NOT available here -- pnpm
# resolves and installs fresh against the npm registry instead of a
# frozen lockfile.
COPY package.json ./
# pnpm >=10 refuses to run dependency postinstall scripts (e.g. esbuild's
# platform-binary download) unless explicitly approved. The first `pnpm
# install` fails but records the pending approval list; `approve-builds
# --all` accepts it non-interactively (no workspace/CI TTY here), then the
# second install completes normally.
RUN pnpm install || true
RUN pnpm approve-builds --all
RUN pnpm install

COPY . .
RUN pnpm run build

# ---- runtime stage: serve the static build output ----
FROM nginx:alpine AS runtime

COPY --from=build /app/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf

EXPOSE 8080
