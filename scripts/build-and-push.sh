#!/usr/bin/env bash
# Builds linux/amd64 images and pushes them to the lab's Artifact Registry with an immutable tag,
# then records the image references for Terraform.
source "$(dirname "$0")/lib.sh"

# Usage: build-and-push.sh [ticketing-api] [load-generator] [control-api] [waiting-room]   (default: all four)
# Only the named images are rebuilt; the others keep their current tag, so for example a dashboard
# change does not roll the application VMs.
TAG="${IMAGE_TAG:-v$(date -u +%Y%m%d-%H%M%S)}"
COMPONENTS="${*:-ticketing-api load-generator control-api waiting-room}"
host="${GCP_REGION}-docker.pkg.dev"

# Short-lived OAuth token piped to docker login; never echoed or written by this script.
gcloud_safe auth print-access-token | docker login -u oauth2accesstoken --password-stdin "https://${host}" >/dev/null
log "Logged in to ${host}"

build() {
    local name="$1" context="$2"; shift 2
    log "Building ${name}:${TAG}"
    docker buildx build --platform linux/amd64 --provenance=false --push \
        --build-arg "APP_VERSION=${TAG}" -t "${REGISTRY}/${name}:${TAG}" "$@" "$context"
}

current() {
    python3 -c "import json,sys; print(json.load(open(sys.argv[1])).get(sys.argv[2], ''))" "${TF_DIR}/images.auto.tfvars.json" "$1" 2>/dev/null || true
}
app_image="$(current app_image)"; loadgen_image="$(current loadgen_image)"; control_image="$(current control_image)"
queue_image="$(current queue_image)"

for c in $COMPONENTS; do
    case "$c" in
        ticketing-api)  build ticketing-api "${REPO_ROOT}/apps/ticketing-api"; app_image="${REGISTRY}/ticketing-api:${TAG}" ;;
        load-generator) build load-generator "${REPO_ROOT}/load-generator"; loadgen_image="${REGISTRY}/load-generator:${TAG}" ;;
        control-api)    build control-api "${REPO_ROOT}/apps" -f "${REPO_ROOT}/apps/control-api/Dockerfile"; control_image="${REGISTRY}/control-api:${TAG}" ;;
        waiting-room)   build waiting-room "${REPO_ROOT}/apps/waiting-room"; queue_image="${REGISTRY}/waiting-room:${TAG}" ;;
        *) die "unknown component: $c" ;;
    esac
done
[ -n "$app_image" ] && [ -n "$loadgen_image" ] && [ -n "$control_image" ] || die "missing image references; build all components once"

cat > "${TF_DIR}/images.auto.tfvars.json" <<JSON
{
  "app_image": "${app_image}",
  "loadgen_image": "${loadgen_image}",
  "control_image": "${control_image}",
  "queue_image": "${queue_image}"
}
JSON
log "Pushed ${COMPONENTS} with tag ${TAG}; recorded in images.auto.tfvars.json"
