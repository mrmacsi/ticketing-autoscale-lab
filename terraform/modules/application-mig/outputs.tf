output "mig_name" { value = google_compute_instance_group_manager.app.name }
output "mig_self_link" { value = google_compute_instance_group_manager.app.self_link }
output "instance_group" { value = google_compute_instance_group_manager.app.instance_group }
output "autoscaler_name" { value = google_compute_autoscaler.app.name }
output "ready_health_check_id" { value = google_compute_health_check.ready.id }
output "base_instance_name" { value = google_compute_instance_group_manager.app.base_instance_name }
