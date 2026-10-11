# Cost and cleanup

All figures are **estimates** from public list prices for `europe-west2` (USD, no discounts). They are not a bill. The dashboard shows the same model (`/api/meta.costModel`, overridable with `PRICE_*` env vars).

## While deployed

| Resource | Quantity | ≈ $/hour | Billed while idle? |
|---|---|---|---|
| Application VMs `n2-standard-2` + 10 GB disk | 1 (min) … 5 (approved max) | 0.115 each | yes, at least `min_instances` |
| Cloud SQL PostgreSQL `db-custom-1-3840`, 10 GB SSD, zonal, no backups | 1 | 0.084 | **yes, 24/7** |
| External Application Load Balancer: forwarding rule + static IP | 1 | 0.035 | **yes, 24/7** (+ ~$0.008/GB processed) |
| Load-generator VM `e2-standard-2` + disk + ephemeral IP | 1 | 0.083 | yes |
| Waiting-room VMs `e2-small` + disk | 2 | 0.022 each | yes |
| Memorystore for Redis, basic tier, 1 GB (waiting-room state) | 1 | 0.058 | **yes, 24/7** |
| Cloud Run control plane, 1 vCPU / 512 MiB, always allocated, min 1 | 1 | 0.068 | **yes, 24/7** |
| Firestore, Secret Manager, Artifact Registry, Logging, Monitoring, PSC endpoint | — | ≈ 0.01 | mostly free tier |

* **Idle (1 app VM): ≈ $0.50/hour ≈ $12/day ≈ $360/month.**
* **Peak (5 app VMs): ≈ $0.96/hour.**
* Sizes were raised on 2026-10-11 (app VMs n1-standard-1 → n2-standard-2, generator e2-small → e2-standard-2, waiting room 1 → 2 VMs + Redis). The earlier measured results in the guides used the smaller sizes; the old idle cost was ≈ $0.29/hour.
* A 10-minute ramp experiment plus 25 minutes of cool-down at up to 5 VMs: **≈ $0.30**.
* Network: synthetic traffic between the generator and the LB in the same region is small (a 10-minute 100 arrivals/s run moves well under 1 GB).

To lower idle cost: set `control_min_instances = 0` (telemetry then only runs while the dashboard is open) or destroy between sessions.

## What keeps costing money after traffic stops

Stopping traffic stops only the generator's requests. These keep running and billing until `terraform destroy`:

* Cloud SQL instance (largest fixed cost)
* load-balancer forwarding rule and static IP
* the minimum number of application VMs (and any the autoscaler has not removed yet)
* the load-generator VM
* the Cloud Run instance (min 1)

## Budget alert

`terraform.tfvars` → `billing_account_id`, `budget_amount` creates a monthly budget scoped to this project and the `lab=ticketing-scale-lab` label with alerts at 50/90/100 %. **A budget alert only sends notifications; it does not stop or cap spending.**

## Guard rails in the lab

* `approved_max_instances` (Terraform) caps the autoscaler and every dashboard override; the control API refuses higher values.
* Experiment limits: ≤ 500 arrivals/s, ≤ 2,000 VUs, ≤ 30 minutes (enforced by both the control API and the generator), hard stop button, max-duration timer independent of the scenario.
* Configurations above 150 arrivals/s, 15 minutes, 1,000 VUs or the approved instance count need an explicit confirmation (HTTP 428 → checkbox).
* The generator can only target the Terraform-provisioned load balancer IP (`ALLOWED_TARGET_HOSTS`); commands cannot change the target.

## Cleanup

**Full teardown (nothing left):** press **Destroy everything in Google Cloud** in the dashboard (open it with `./scripts/dashboard.sh`) and type `destroy ticketing-scale-lab`, or run:

```bash
./scripts/teardown.sh --yes
```

It stops traffic, runs `terraform destroy`, removes the temporary `roles/datastore.owner` grant made for the deployment, checks every resource type for anything named `ticketing-scale-lab` / `tsl-*` (aborting if something is left), then disables the APIs the lab enabled (Cloud SQL Admin, Cloud Run, Artifact Registry, Secret Manager, Firestore, IAP, Billing Budgets). APIs that were already on before the lab, plus `iam` and `cloudresourcemanager`, stay enabled. The button only exists when the dashboard is opened through the local proxy, which runs the script on your machine (where the Terraform state is). It accepts the request only from the dashboard page itself, with the typed phrase.

Teardown runs in stages: delete the app VMs, generator, control service and SQL instance first; wait for Cloud Run's internal IP reservations to disappear; then delete the remaining network and resources. Google can retain Direct VPC egress IPs for **1–2 hours** after service deletion ([Google documentation](https://docs.cloud.google.com/run/docs/configuring/vpc-direct-vpc#troubleshooting)). Keep the teardown process running during this wait. The default deadline is 150 minutes; override it with `TEARDOWN_VPC_RELEASE_TIMEOUT_SECONDS` if needed. If the wait times out, infrastructure deletion fails, or a resource verification request fails, teardown exits unsuccessfully and can be resumed with the same command. APIs are disabled only after successful infrastructure deletion and verification, and failed verification requests count as failures rather than empty inventories.

The database and PostgreSQL roles use `deletion_policy = "ABANDON"`: Terraform skips separate database/role drops, and deleting the **dedicated SQL instance** removes them together. This avoids active-connection and dependent-object errors. The scripts also migrate older local state during a confirmed full teardown, after checking the parent instance's project, name and deletion protection. This policy applies to individual resource removal too: removing a database or role from configuration while retaining the instance leaves that child in PostgreSQL.

Cleanup Terraform commands use HTTPS over HTTP/1.1 and parallelism 2 to reduce transport failures on unstable networks; other Terraform commands and application traffic are unaffected. [Go documents the HTTP client setting](https://pkg.go.dev/net/http). Destroy planning retries temporary Google API transport failures (such as `EOF`, connection resets or unreachable routes) up to three times, with a fresh OAuth token and a new plan each time. Each deletion phase also recovers from temporary transport errors up to three times by refreshing state, rebuilding and validating a new destroy plan. A failed apply's saved plan is never reused: Google may already have completed a deletion whose polling failed. Permission, configuration and resource dependency errors still stop immediately.

Offline regression checks: `python3 -m unittest discover -s scripts/tests -v` and `terraform -chdir=terraform/modules/database test`.

**Infrastructure only (APIs and the role stay):**

```bash
./scripts/terraform-destroy.sh
```

The script shows the recorded destroy scope (only resources in `terraform/environments/autoscaling-lab/terraform.tfstate`), asks you to type `destroy ticketing-scale-lab`, uses the same staged infrastructure deletion and IP-release wait, then lists any remaining VMs labelled `lab=ticketing-scale-lab` (should be none).

Not removed on purpose: project APIs enabled by Terraform stay enabled (`disable_on_destroy = false`) because other workloads in the project may rely on them. Images in Artifact Registry are removed with the repository.
