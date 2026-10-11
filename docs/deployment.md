# Deployment

Everything runs as the authorised account `thebeezindex@gmail.com` in project `contentpulse-vertex-ai` (number `210564622269`), region `europe-west2`, zone `europe-west2-b`. Scripts call `gcloud` with explicit `--account` and `--project` (`scripts/lib.sh` → `gcloud_safe`) and never change the global gcloud configuration.

## Prerequisites

* gcloud at `/opt/homebrew/share/google-cloud-sdk/bin/gcloud`, logged in as the authorised account (`gcloud auth login thebeezindex@gmail.com` if expired)
* Terraform ≥ 1.9, Docker with buildx (images are built for `linux/amd64`), Node 22+, Go 1.24+, PHP 8.4 + Composer (tests only)
* Caller permissions checked by `scripts/gcp-preflight.sh`. The account has Editor + Project IAM Admin, which covers everything except **`datastore.databases.create`** (needed once to create the Firestore database). Minimum fix, run by the project owner:

  ```bash
  gcloud --account=thebeezindex@gmail.com --project=contentpulse-vertex-ai projects add-iam-policy-binding contentpulse-vertex-ai --member=user:thebeezindex@gmail.com --role=roles/datastore.owner
  ```

## Terraform authentication

Before every `plan`, `apply`, `refresh` or `destroy`, the scripts export a fresh token **in the same shell process** and clear other credential variables:

```bash
export GOOGLE_OAUTH_ACCESS_TOKEN="$(gcloud --account=thebeezindex@gmail.com --project=contentpulse-vertex-ai auth print-access-token)"
```

The token is never printed, logged or written. The provider block sets `project`, `region`, `zone`, `billing_project` and `user_project_override = true`; there are no key files and no impersonation.

State: `terraform/environments/autoscaling-lab/terraform.tfstate` (local, gitignored, contains generated secrets — keep it private).

## Steps

```bash
cp terraform/environments/autoscaling-lab/terraform.tfvars.example terraform/environments/autoscaling-lab/terraform.tfvars   # already present
./scripts/gcp-preflight.sh                      # read-only checks
./scripts/approve.sh 5 "<budget>"               # record the owner's approval of max VMs and budget
./scripts/deploy.sh                             # everything below, in order
```

`deploy.sh` runs:

1. preflight (account, project number, billing, APIs, permissions, quotas, machine type, name conflicts);
2. `terraform fmt -check`, `validate`, plan of the **foundation** stage (`enable_workloads=false`: APIs, VPC, Artifact Registry, secrets, Cloud SQL + PSC endpoint, Firestore, service accounts, IAM) with a summary of every create/update/delete;
3. approval check (`.approved-budget` must exist and cover `max_instances`/`approved_max_instances`);
4. apply foundation;
5. build and push three `linux/amd64` images with an immutable tag (`build-and-push.sh` writes `images.auto.tfvars.json`);
6. plan + apply workloads (instance template, MIG, autoscaler, health checks, load balancer, generator VM, Cloud Run, monitoring dashboard);
7. VMs boot COS, pull the image, read secrets, run migrations under an advisory lock, pass health checks and join the backend;
8. `verify.sh --wait`: healthy VMs, LB backend health, `/health` through the LB, `X-Served-By`, admin API blocked through the LB (403), autoscaler configuration, Cloud Run ready, anonymous dashboard access refused, generator heartbeat, service-account roles;
9. prints the application and dashboard URLs.

Open the dashboard:

```bash
./scripts/dashboard.sh        # http://localhost:8090
```

`dashboard.sh` runs a small local proxy (`scripts/dashboard-proxy.mjs`) that signs every request with an identity token of the authorised account. `gcloud run services proxy` is not used because it authenticates as the *globally active* gcloud account, which is not on the allowlist.

## Problems found during the first deployment (fixed)

| Symptom | Cause | Fix |
|---|---|---|
| `Cloud SQL Admin API has not been used…` on the first apply | APIs enabled in the same apply had not propagated | explicit `depends_on` on the API resources; re-run |
| VMs could not pull images (`artifactregistry.repositories.downloadArtifacts denied`) | Artifact Registry ignores the resource-name IAM condition for pulls | `artifactregistry.reader` granted without condition (read-only) |
| `fe_sendauth: no password supplied` | entrypoint called Secret Manager without `/versions/latest` and swallowed the 404 | versioned URL; the entrypoint now fails loudly with the HTTP status |
| Health checks time out, VM recreated in a loop | Container-Optimized OS drops inbound traffic by default; migrations blocked nginx start | `iptables` rule for the app port in the unit; migrations run as a supervised task |
| Reservation sweeper failing (`/root/.postgresql/postgresql.crt`) | libpq looks for client certs under `$HOME`, inherited as `/root` | `HOME=/var/www` for www-data processes |
| LB `503 backend_connection_closed_before_data_sent_to_client` under bursts | nginx `1024 worker_connections are not enough` | 16,384 connections per worker, PHP-FPM 32 workers |

## Redeploying a new application version

```bash
./scripts/build-and-push.sh && ./scripts/terraform-plan.sh && ./scripts/terraform-apply.sh
```

The new instance template triggers a proactive rolling replacement (surge 1, unavailable 0).

## Optional: Google Sign-In instead of the IAM proxy

Create an OAuth "Web application" client in the console (authorised JavaScript origin = the Cloud Run URL), then set `auth_mode = "google_signin"` and `google_client_id`. In this mode Cloud Run must allow unauthenticated invocations so the browser can load the page; the API verifies the Google ID token (signature, audience, expiry, verified email) and the `allowed_emails` allowlist on every request. This requires granting `roles/run.invoker` to `allUsers`, which the default deployment deliberately does not do.
