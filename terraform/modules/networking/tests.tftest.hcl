mock_provider "google" {}

variables {
  prefix         = "ticketing-scale-lab"
  region         = "europe-west2"
  workloads_cidr = "10.60.0.0/20"
  control_cidr   = "10.60.16.0/24"
  app_tag        = "app"
  loadgen_tag    = "loadgen"
  queue_tag      = "queue"
  app_port       = 8080
  queue_port     = 8080
}

run "firewall_exposure_is_minimal" {
  command = plan

  assert {
    condition     = toset(google_compute_firewall.lb_to_app.source_ranges) == toset(["35.191.0.0/16", "130.211.0.0/22"])
    error_message = "only Google load-balancer ranges may reach the app port from outside the VPC"
  }
  assert {
    condition     = toset(google_compute_firewall.iap_ssh.source_ranges) == toset(["35.235.240.0/20"])
    error_message = "SSH only via IAP"
  }
  assert {
    condition     = google_compute_subnetwork.workloads.private_ip_google_access
    error_message = "private Google access required for VMs without external IPs"
  }
}

run "waiting_room_reachable_only_from_lb_and_control_plane" {
  command = plan

  assert {
    condition     = toset(google_compute_firewall.lb_to_queue.source_ranges) == toset(["35.191.0.0/16", "130.211.0.0/22"])
    error_message = "only Google load-balancer ranges may reach the waiting room from outside the VPC"
  }
  assert {
    condition     = toset(google_compute_firewall.control_to_queue.source_ranges) == toset(["10.60.16.0/24"])
    error_message = "only the control subnet may reach the waiting room admin API"
  }
  assert {
    condition     = toset(google_compute_firewall.queue_to_app.source_tags) == toset(["queue"])
    error_message = "queue-to-app access must be limited to the queue VM"
  }
}
