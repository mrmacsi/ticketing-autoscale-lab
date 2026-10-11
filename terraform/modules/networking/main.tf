# Dedicated VPC for the lab. Application VMs have no external IPs; Google APIs (Artifact
# Registry, Secret Manager, Logging) are reached through Private Google Access.

resource "google_compute_network" "lab" {
  name                    = "${var.prefix}-vpc"
  auto_create_subnetworks = false
  routing_mode            = "REGIONAL"
}

resource "google_compute_subnetwork" "workloads" {
  name                     = "${var.prefix}-workloads"
  region                   = var.region
  network                  = google_compute_network.lab.id
  ip_cidr_range            = var.workloads_cidr
  private_ip_google_access = true
}

# Cloud Run Direct VPC egress allocates control-plane IPs from this range.
resource "google_compute_subnetwork" "control" {
  name                     = "${var.prefix}-control"
  region                   = var.region
  network                  = google_compute_network.lab.id
  ip_cidr_range            = var.control_cidr
  private_ip_google_access = true
}

# Google Front End proxies and health checkers of the external Application Load Balancer.
resource "google_compute_firewall" "lb_to_app" {
  name          = "${var.prefix}-allow-lb-health-and-traffic"
  network       = google_compute_network.lab.id
  direction     = "INGRESS"
  source_ranges = ["35.191.0.0/16", "130.211.0.0/22"]
  target_tags   = [var.app_tag]
  allow {
    protocol = "tcp"
    ports    = [tostring(var.app_port)]
  }
}

# The control plane reads /instance-info and calls the private admin API on VM private IPs.
resource "google_compute_firewall" "control_to_app" {
  name          = "${var.prefix}-allow-control-to-app"
  network       = google_compute_network.lab.id
  direction     = "INGRESS"
  source_ranges = [var.control_cidr]
  target_tags   = [var.app_tag]
  allow {
    protocol = "tcp"
    ports    = [tostring(var.app_port)]
  }
}

# Load balancer proxies and health checkers reach the waiting room.
resource "google_compute_firewall" "lb_to_queue" {
  name          = "${var.prefix}-allow-lb-to-queue"
  network       = google_compute_network.lab.id
  direction     = "INGRESS"
  source_ranges = ["35.191.0.0/16", "130.211.0.0/22"]
  target_tags   = [var.queue_tag]
  allow {
    protocol = "tcp"
    ports    = [tostring(var.queue_port)]
  }
}

# The control plane configures the waiting room and reads its stats (/admin/*, admin token).
resource "google_compute_firewall" "control_to_queue" {
  name          = "${var.prefix}-allow-control-to-queue"
  network       = google_compute_network.lab.id
  direction     = "INGRESS"
  source_ranges = [var.control_cidr]
  target_tags   = [var.queue_tag]
  allow {
    protocol = "tcp"
    ports    = [tostring(var.queue_port)]
  }
}

# The waiting room reads ticket availability from healthy app VMs on their private IPs.
resource "google_compute_firewall" "queue_to_app" {
  name        = "${var.prefix}-allow-queue-to-app"
  network     = google_compute_network.lab.id
  direction   = "INGRESS"
  source_tags = [var.queue_tag]
  target_tags = [var.app_tag]
  allow {
    protocol = "tcp"
    ports    = [tostring(var.app_port)]
  }
}

# SSH only through Identity-Aware Proxy TCP forwarding, for troubleshooting.
resource "google_compute_firewall" "iap_ssh" {
  name          = "${var.prefix}-allow-iap-ssh"
  network       = google_compute_network.lab.id
  direction     = "INGRESS"
  source_ranges = ["35.235.240.0/20"]
  target_tags   = [var.app_tag, var.loadgen_tag, var.queue_tag]
  allow {
    protocol = "tcp"
    ports    = ["22"]
  }
}
