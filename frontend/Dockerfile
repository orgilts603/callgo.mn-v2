# syntax=docker/dockerfile:1.7
# =============================================================================
# CallGo.mn frontend — multi-stage build: Node (pnpm + Vite) -> nginx.
# Build context: ./frontend  (docker compose: infra/docker-compose.yml)
#
# Only the files the build needs are copied, so a local node_modules/ or dist/
# in the context never leaks into the image.
# =============================================================================

ARG NODE_IMAGE=node:22-alpine
ARG NGINX_IMAGE=nginx:1.30-alpine

# ---- build stage -----------------------------------------------------------
FROM ${NODE_IMAGE} AS build
WORKDIR /app
ENV CI=true
ARG PNPM_VERSION=10.33.0
RUN npm install -g "pnpm@${PNPM_VERSION}"

# Dependency layer (cached until package.json / lockfile change).
COPY package.json pnpm-lock.yaml ./
RUN --mount=type=cache,id=callgo-pnpm-store,target=/root/.local/share/pnpm/store \
    pnpm install --frozen-lockfile

# Sources. The app talks to the backend via relative /api URLs, so no
# VITE_* build-time variables are needed.
COPY index.html vite.config.ts tsconfig.json tsconfig.app.json tsconfig.node.json ./
COPY public ./public
COPY src ./src
RUN pnpm build

# ---- runtime stage ---------------------------------------------------------
FROM ${NGINX_IMAGE} AS runtime
# Server block (SPA + /api proxy + /api/ws WebSocket upgrade).
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 80
HEALTHCHECK --interval=15s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1/healthz || exit 1
