#!/usr/bin/env bash
# Opens the IAM-protected dashboard at http://localhost:8090 through a local proxy that signs every
# request with an identity token of the authorised account (Cloud Run IAM checks run.invoker).
source "$(dirname "$0")/lib.sh"
PORT="${PORT:-8090}"
url="$(gcloud_safe run services describe "${PREFIX}-control" --region="$GCP_REGION" --format='value(status.url)')"
[ -n "$url" ] || die "Control plane is not deployed"
export GCLOUD GCP_ACCOUNT GCP_PROJECT
exec node "$(dirname "$0")/dashboard-proxy.mjs" "$url" "$PORT"
