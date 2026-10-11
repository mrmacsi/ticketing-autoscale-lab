#!/usr/bin/env bash
# Calls the control API as the authorised account. Usage: control.sh METHOD PATH [JSON_BODY]
source "$(dirname "$0")/lib.sh"
url="$(gcloud_safe run services describe "${PREFIX}-control" --region="$GCP_REGION" --format='value(status.url)')"
[ -n "$url" ] || die "Control plane is not deployed"
token="$(gcloud_safe auth print-identity-token)"
args=(-fsS -X "$1" -H "Authorization: Bearer ${token}" "${url}$2")
[ $# -ge 3 ] && args+=(-H 'Content-Type: application/json' -d "$3")
curl "${args[@]}"
