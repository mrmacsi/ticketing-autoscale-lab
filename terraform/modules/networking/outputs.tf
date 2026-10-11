output "network_id" { value = google_compute_network.lab.id }
output "network_self_link" { value = google_compute_network.lab.self_link }
output "workloads_subnet_id" { value = google_compute_subnetwork.workloads.id }
output "control_subnet_id" { value = google_compute_subnetwork.control.id }
