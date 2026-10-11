output "application_url" {
  description = "Public load-balancer URL of the ticketing API."
  value       = try(module.load_balancer[0].url, null)
}

output "dashboard_url" {
  description = "Cloud Run URL (IAM-protected; open through scripts/dashboard.sh)."
  value       = try(module.control_plane[0].url, null)
}

output "control_service_name" {
  value = try(module.control_plane[0].service_name, null)
}

output "mig_name" {
  value = try(module.application[0].mig_name, null)
}

output "autoscaler_name" {
  value = try(module.application[0].autoscaler_name, null)
}

output "waiting_room" {
  description = "Waiting-room VM and its private admin URL (null when not deployed)."
  value = local.waiting_room ? {
    instance    = module.waiting_room[0].instance_name
    private_url = module.waiting_room[0].private_url
    public_path = "${module.load_balancer[0].url}/queue/"
  } : null
}

output "loadgen_instance" {
  value = try(module.load_generator[0].instance_name, null)
}

output "artifact_registry" {
  value = local.registry
}

output "database" {
  description = "Connection reference only; passwords live in Secret Manager."
  value = {
    instance         = module.database.instance_name
    connection_name  = module.database.connection_name
    private_endpoint = module.database.host
    database         = module.database.database_name
    app_user         = module.database.app_user
    app_secret       = google_secret_manager_secret.lab["db-app-password"].secret_id
    observer_user    = module.database.observer_user
    observer_secret  = google_secret_manager_secret.lab["db-observer-password"].secret_id
  }
}

output "firestore_database" {
  value = google_firestore_database.lab.name
}

output "monitoring_links" {
  value = {
    dashboard       = try("https://console.cloud.google.com/monitoring/dashboards/builder/${element(split("/", google_monitoring_dashboard.lab[0].id), 3)}?project=${var.project_id}", null)
    mig             = "https://console.cloud.google.com/compute/instanceGroups/details/${var.zone}/${var.prefix}-app-mig?project=${var.project_id}"
    autoscaler_logs = "https://console.cloud.google.com/logs/query;query=resource.type%3D%22autoscaler%22?project=${var.project_id}"
    app_logs        = "https://console.cloud.google.com/logs/query;query=labels.%22compute.googleapis.com%2Fresource_name%22:%22${var.prefix}-app%22?project=${var.project_id}"
    cloud_sql       = "https://console.cloud.google.com/sql/instances/${module.database.instance_name}/overview?project=${var.project_id}"
  }
}

output "commands" {
  value = {
    deploy    = "./scripts/deploy.sh"
    dashboard = "./scripts/dashboard.sh"
    verify    = "./scripts/verify.sh"
    plan      = "./scripts/terraform-plan.sh"
    destroy   = "./scripts/terraform-destroy.sh"
  }
}
