#!/usr/bin/env bash
# Single entry point: preflight → validate → plan → approval check → foundation → images →
# workloads → health/routing/autoscaler verification → URLs.
source "$(dirname "$0")/lib.sh"
cd "$REPO_ROOT"

log "Step 1: preflight"
./scripts/gcp-preflight.sh

log "Step 2-4: validate, plan foundation, check approval"
require_approval
./scripts/terraform-plan.sh --foundation
log "Step 5a: provision foundation (APIs, network, registry, secrets, Cloud SQL, Firestore)"
./scripts/terraform-apply.sh

log "Step 6: build and push images"
./scripts/build-and-push.sh

log "Step 5b/7: provision application, load balancer, generator, control plane"
./scripts/terraform-plan.sh
./scripts/terraform-apply.sh

log "Step 8-11: migrations run on first VM boot (advisory-locked); verifying health, routing, autoscaler"
./scripts/verify.sh --wait

log "Step 12-13: endpoints"
echo "Application (load balancer): $(tf_output application_url)"
echo "Dashboard (Cloud Run, IAM):  $(tf_output dashboard_url)"
echo "Open the dashboard with:     ./scripts/dashboard.sh   (http://localhost:8090)"
