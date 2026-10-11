# syntax=docker/dockerfile:1.7
# Build context: apps/ (the image bundles the dashboard's static build with the control API).
FROM node:22-alpine AS dashboard
WORKDIR /dashboard
COPY dashboard/package.json dashboard/package-lock.json dashboard/.npmrc ./
RUN npm ci --no-audit --no-fund
COPY dashboard/ ./
RUN npm run build

FROM node:22-alpine AS api
WORKDIR /api
COPY control-api/package.json control-api/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY control-api/tsconfig.json ./
COPY control-api/src ./src
RUN npm run build && npm prune --omit=dev

FROM node:22-alpine
ENV NODE_ENV=production STATIC_DIR=/app/public PORT=8080
WORKDIR /app
COPY --from=api /api/package.json ./
COPY --from=api /api/node_modules ./node_modules
COPY --from=api /api/dist ./dist
COPY --from=dashboard /dashboard/dist ./public
USER node
EXPOSE 8080
CMD ["node", "dist/index.js"]
