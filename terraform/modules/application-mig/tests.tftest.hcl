mock_provider "google" {}

variables {
  prefix                         = "ticketing-scale-lab"
  zone                           = "europe-west2-b"
  subnet_id                      = "projects/p/regions/europe-west2/subnetworks/s"
  image                          = "europe-west2-docker.pkg.dev/p/r/ticketing-api:v1"
  machine_type                   = "n1-standard-1"
  app_tag                        = "ticketing-scale-lab-app"
  app_port                       = 8080
  labels                         = { lab = "ticketing-scale-lab" }
  initial_instances              = 1
  min_instances                  = 1
  max_instances                  = 5
  target_cpu_utilization         = 0.6
  instance_init_seconds          = 60
  scale_in_stabilization_seconds = 240
  fpm_max_children               = 16
  db_host                        = "10.60.0.5"
  db_name                        = "ticketing"
  db_user                        = "ticketing_app"
  db_password_secret             = "projects/p/secrets/db"
  admin_token_secret             = "projects/p/secrets/admin"
  app_key_secret                 = "projects/p/secrets/key"
  admin_allow_cidrs              = ["10.60.16.0/24"]
  service_account_email          = "app@p.iam.gserviceaccount.com"
}

run "autoscaler_min_max_and_cpu_rules" {
  command = plan

  assert {
    condition     = google_compute_autoscaler.app.autoscaling_policy[0].min_replicas == 1 && google_compute_autoscaler.app.autoscaling_policy[0].max_replicas == 5
    error_message = "autoscaler bounds wrong"
  }
  assert {
    condition     = google_compute_autoscaler.app.autoscaling_policy[0].cpu_utilization[0].target == 0.6
    error_message = "CPU target wrong"
  }
  assert {
    condition     = google_compute_autoscaler.app.autoscaling_policy[0].mode == "ON" && google_compute_autoscaler.app.autoscaling_policy[0].cooldown_period == 60
    error_message = "autoscaler must be ON with the configured initialisation period"
  }
  assert {
    condition     = google_compute_autoscaler.app.autoscaling_policy[0].stabilization_period == 240
    error_message = "scale-in stabilisation not applied"
  }
}

run "mig_starts_with_one_vm_and_autoheals_on_liveness" {
  command = plan

  assert {
    condition     = google_compute_instance_group_manager.app.target_size == 1
    error_message = "MIG must start with the initial instance count"
  }
  assert {
    condition     = one([for p in google_compute_instance_group_manager.app.named_port : p.port if p.name == "http"]) == 8080
    error_message = "named port must match the application port"
  }
  assert {
    condition     = google_compute_health_check.live.http_health_check[0].request_path == "/health/live" && google_compute_health_check.ready.http_health_check[0].request_path == "/health"
    error_message = "liveness/readiness paths wrong"
  }
}

run "vms_are_private_and_bootstrap_the_container" {
  command = plan

  assert {
    condition     = length(google_compute_instance_template.app.network_interface[0].access_config) == 0
    error_message = "application VMs must not have external IPs"
  }
  assert {
    condition     = strcontains(google_compute_instance_template.app.metadata["user-data"], "docker run --rm --name ticketing --network host")
    error_message = "cloud-init must start the application container"
  }
  assert {
    condition     = strcontains(google_compute_instance_template.app.metadata["user-data"], "ADMIN_ALLOW_CIDRS=127.0.0.1/32,10.60.16.0/24")
    error_message = "admin API must be limited to the control subnet"
  }
  assert {
    condition     = !strcontains(google_compute_instance_template.app.metadata["user-data"], "DB_PASSWORD=")
    error_message = "no secret values may be placed in instance metadata"
  }
}
