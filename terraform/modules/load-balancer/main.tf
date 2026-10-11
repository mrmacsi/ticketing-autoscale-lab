# Global external Application Load Balancer (Envoy-based, EXTERNAL_MANAGED) in front of the MIG.
# Plain HTTP: the lab has no domain for a managed certificate and carries only synthetic data.

resource "google_compute_global_address" "lb" {
  name   = "${var.prefix}-lb-ip"
  labels = var.labels
}

resource "google_compute_backend_service" "app" {
  name                            = "${var.prefix}-app-backend"
  load_balancing_scheme           = "EXTERNAL_MANAGED"
  protocol                        = "HTTP"
  port_name                       = "http"
  timeout_sec                     = 30
  connection_draining_timeout_sec = 30
  health_checks                   = [var.health_check_id]
  locality_lb_policy              = "ROUND_ROBIN"

  backend {
    group           = var.instance_group
    balancing_mode  = "UTILIZATION"
    max_utilization = 1.0
    capacity_scaler = 1.0
  }

  log_config {
    enable      = true
    sample_rate = var.log_sample_rate
  }
}

# Everything goes to the application, except /queue/* which goes to the waiting room when it exists.
resource "google_compute_url_map" "app" {
  name            = "${var.prefix}-url-map"
  default_service = google_compute_backend_service.app.id

  dynamic "host_rule" {
    for_each = var.waiting_room_backend_id == null ? [] : [1]
    content {
      hosts        = ["*"]
      path_matcher = "lab"
    }
  }

  dynamic "path_matcher" {
    for_each = var.waiting_room_backend_id == null ? [] : [1]
    content {
      name            = "lab"
      default_service = google_compute_backend_service.app.id
      path_rule {
        paths   = ["/queue", "/queue/*"]
        service = var.waiting_room_backend_id
      }
    }
  }
}

resource "google_compute_target_http_proxy" "app" {
  name    = "${var.prefix}-http-proxy"
  url_map = google_compute_url_map.app.id
}

resource "google_compute_global_forwarding_rule" "http" {
  name                  = "${var.prefix}-http"
  load_balancing_scheme = "EXTERNAL_MANAGED"
  ip_protocol           = "TCP"
  port_range            = "80"
  target                = google_compute_target_http_proxy.app.id
  ip_address            = google_compute_global_address.lb.id
  labels                = var.labels
}
