# Control API + dashboard on Cloud Run, separate from the autoscaled application VMs.
# Invocation requires Cloud Run IAM (roles/run.invoker granted only to the allowlist).

resource "google_cloud_run_v2_service" "control" {
  name                = "${var.prefix}-control"
  location            = var.region
  ingress             = "INGRESS_TRAFFIC_ALL"
  deletion_protection = false
  labels              = var.labels
  # Keep the IAM invoker check on (the default). Only allowlisted principals may call it.
  invoker_iam_disabled = false

  template {
    service_account                  = var.service_account_email
    timeout                          = "3600s"
    max_instance_request_concurrency = 200
    labels                           = var.labels

    # Exactly one instance: it owns the telemetry pollers and the SSE fan-out.
    scaling {
      min_instance_count = var.min_instances
      max_instance_count = 1
    }

    vpc_access {
      egress = "PRIVATE_RANGES_ONLY"
      network_interfaces {
        network    = var.network_id
        subnetwork = var.subnet_id
      }
    }

    containers {
      image = var.image
      ports {
        container_port = 8080
      }
      resources {
        limits = {
          cpu    = "1"
          memory = "512Mi"
        }
        # CPU stays allocated between requests so telemetry keeps flowing with no browser open.
        cpu_idle          = false
        startup_cpu_boost = true
      }

      dynamic "env" {
        for_each = var.env
        content {
          name  = env.key
          value = env.value
        }
      }
      env {
        name = "DB_PASSWORD"
        value_source {
          secret_key_ref {
            secret  = var.observer_password_secret_id
            version = "latest"
          }
        }
      }
      env {
        name = "LAB_ADMIN_TOKEN"
        value_source {
          secret_key_ref {
            secret  = var.admin_token_secret_id
            version = "latest"
          }
        }
      }

      startup_probe {
        http_get {
          path = "/healthz"
        }
        period_seconds    = 3
        failure_threshold = 20
      }
    }
  }
}
