# Least-privilege service accounts. Resource-specific grants are project-level bindings with IAM
# conditions on resource names (the deploying account may set project IAM but not resource IAM).

resource "google_service_account" "app" {
  account_id   = "${var.short_prefix}-app"
  display_name = "Ticketing lab application VMs"
  depends_on   = [google_project_service.apis]
}

resource "google_service_account" "loadgen" {
  account_id   = "${var.short_prefix}-loadgen"
  display_name = "Ticketing lab load generator"
  depends_on   = [google_project_service.apis]
}

resource "google_service_account" "queue" {
  account_id   = "${var.short_prefix}-queue"
  display_name = "Ticketing lab waiting room"
  depends_on   = [google_project_service.apis]
}

resource "google_service_account" "control" {
  account_id   = "${var.short_prefix}-control"
  display_name = "Ticketing lab control plane (Cloud Run)"
  depends_on   = [google_project_service.apis]
}

locals {
  secret_prefix     = "projects/${var.project_number}/secrets/${var.prefix}-"
  firestore_db_name = "projects/${var.project_id}/databases/${local.firestore_db}"

  secret_condition = { for sa, names in {
    app     = ["db-app-password", "admin-token", "app-key"]
    control = ["db-observer-password", "admin-token"]
    queue   = ["admin-token"]
  } : sa => join(" || ", [for n in names : "resource.name.startsWith(\"${local.secret_prefix}${n}\")"]) }

  firestore_condition = "resource.name.startsWith(\"${local.firestore_db_name}\")"

  app_roles = {
    "logs"    = { role = "roles/logging.logWriter", condition = null }
    "metrics" = { role = "roles/monitoring.metricWriter", condition = null }
    # Artifact Registry does not evaluate resource-name conditions for image pulls, so read access is project-wide (read-only).
    "registry" = { role = "roles/artifactregistry.reader", condition = null }
    "secrets"  = { role = "roles/secretmanager.secretAccessor", condition = local.secret_condition.app }
  }
  loadgen_roles = {
    "logs"      = { role = "roles/logging.logWriter", condition = null }
    "metrics"   = { role = "roles/monitoring.metricWriter", condition = null }
    "registry"  = { role = "roles/artifactregistry.reader", condition = null }
    "firestore" = { role = "roles/datastore.user", condition = local.firestore_condition }
  }
  queue_roles = {
    "logs"     = { role = "roles/logging.logWriter", condition = null }
    "metrics"  = { role = "roles/monitoring.metricWriter", condition = null }
    "registry" = { role = "roles/artifactregistry.reader", condition = null }
    # Reads the load balancer's backend health (getHealth) to know how many servers can take customers.
    "compute-read" = { role = "roles/compute.viewer", condition = null }
    "secrets"      = { role = "roles/secretmanager.secretAccessor", condition = local.secret_condition.queue }
  }
  control_roles = {
    "compute-read" = { role = "roles/compute.viewer", condition = null }
    # Only the lab's autoscaler and MIG can be modified (runtime experiment overrides).
    "autoscaler-write" = {
      role = "roles/compute.instanceAdmin.v1"
      condition = join(" || ", [
        "resource.name.startsWith(\"projects/${var.project_id}/zones/${var.zone}/autoscalers/${var.prefix}-\")",
        "resource.name.startsWith(\"projects/${var.project_id}/zones/${var.zone}/instanceGroupManagers/${var.prefix}-\")",
      ])
    }
    "monitoring" = { role = "roles/monitoring.viewer", condition = null }
    "firestore"  = { role = "roles/datastore.user", condition = local.firestore_condition }
    "secrets"    = { role = "roles/secretmanager.secretAccessor", condition = local.secret_condition.control }
    "logs"       = { role = "roles/logging.logWriter", condition = null }
  }
}

resource "google_project_iam_member" "app" {
  for_each = local.app_roles
  project  = var.project_id
  role     = each.value.role
  member   = "serviceAccount:${google_service_account.app.email}"
  dynamic "condition" {
    for_each = each.value.condition == null ? [] : [each.value.condition]
    content {
      title      = "${var.prefix}-app-${each.key}"
      expression = condition.value
    }
  }
}

resource "google_project_iam_member" "loadgen" {
  for_each = local.loadgen_roles
  project  = var.project_id
  role     = each.value.role
  member   = "serviceAccount:${google_service_account.loadgen.email}"
  dynamic "condition" {
    for_each = each.value.condition == null ? [] : [each.value.condition]
    content {
      title      = "${var.prefix}-loadgen-${each.key}"
      expression = condition.value
    }
  }
}

resource "google_project_iam_member" "queue" {
  for_each = local.queue_roles
  project  = var.project_id
  role     = each.value.role
  member   = "serviceAccount:${google_service_account.queue.email}"
  dynamic "condition" {
    for_each = each.value.condition == null ? [] : [each.value.condition]
    content {
      title      = "${var.prefix}-queue-${each.key}"
      expression = condition.value
    }
  }
}

resource "google_project_iam_member" "control" {
  for_each = local.control_roles
  project  = var.project_id
  role     = each.value.role
  member   = "serviceAccount:${google_service_account.control.email}"
  dynamic "condition" {
    for_each = each.value.condition == null ? [] : [each.value.condition]
    content {
      title      = "${var.prefix}-control-${each.key}"
      expression = condition.value
    }
  }
}

# Dashboard access: only the allowlisted Google accounts may invoke the control-plane service.
resource "google_project_iam_member" "dashboard_invoker" {
  for_each = toset(var.allowed_emails)
  project  = var.project_id
  role     = "roles/run.invoker"
  member   = "user:${each.value}"
  condition {
    title      = "${var.prefix}-dashboard-invoker"
    expression = "resource.name == \"projects/${var.project_id}/locations/${var.region}/services/${var.prefix}-control\""
  }
}
