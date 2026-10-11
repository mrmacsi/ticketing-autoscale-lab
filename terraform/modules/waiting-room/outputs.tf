output "backend_service_id" { value = google_compute_backend_service.queue.id }
output "private_ip" { value = google_compute_address.queue.address }
output "private_url" { value = "http://${google_compute_address.queue.address}:${var.port}" }
output "instance_name" { value = google_compute_instance.queue.name }
