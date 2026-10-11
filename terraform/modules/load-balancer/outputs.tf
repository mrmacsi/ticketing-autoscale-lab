output "ip_address" { value = google_compute_global_address.lb.address }
output "url" { value = "http://${google_compute_global_address.lb.address}" }
output "backend_service_name" { value = google_compute_backend_service.app.name }
output "backend_service_self_link" { value = google_compute_backend_service.app.self_link }
