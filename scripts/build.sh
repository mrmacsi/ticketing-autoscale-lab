#!/usr/bin/env bash
# Runs every test suite and builds all images locally (no push, no cloud access).
source "$(dirname "$0")/lib.sh"
cd "$REPO_ROOT"
log "Ticketing API tests (needs PostgreSQL on 127.0.0.1:55432 — see README)"
(cd apps/ticketing-api && composer install -q && php artisan test)
log "Control API tests";       (cd apps/control-api && npm ci --silent && npm run typecheck && npm test)
log "Dashboard tests";         (cd apps/dashboard && npm ci --silent && npm run typecheck && npm test && npm run build)
log "Load generator tests";    (cd load-generator && go vet ./... && go test -race ./...)
log "Terraform tests"
terraform -chdir=terraform/environments/autoscaling-lab init -backend=false -input=false >/dev/null && terraform -chdir=terraform/environments/autoscaling-lab test
for m in application-mig database networking; do terraform -chdir="terraform/modules/$m" init -backend=false -input=false >/dev/null && terraform -chdir="terraform/modules/$m" test; done
log "Images";                  docker compose build
