# Stateless Laravel application VMs: Container-Optimized OS pulls the versioned image from
# Artifact Registry at boot and runs it under systemd. No SSH or manual steps are needed.

locals {
  registry_host = split("/", var.image)[0]
  env_file = join("\n", [for k, v in merge({
    DB_HOST                     = var.db_host
    DB_PORT                     = "5432"
    DB_DATABASE                 = var.db_name
    DB_USERNAME                 = var.db_user
    DB_SSLMODE                  = "require"
    DB_PERSISTENT               = "true"
    DB_PASSWORD_SECRET          = var.db_password_secret
    LAB_ADMIN_TOKEN_SECRET      = var.admin_token_secret
    APP_KEY_SECRET              = var.app_key_secret
    ADMIN_ALLOW_CIDRS           = join(",", concat(["127.0.0.1/32"], var.admin_allow_cidrs))
    LOAD_ALLOW_CIDRS            = join(",", concat(["127.0.0.1/32"], var.load_allow_cidrs))
    HTTP_PORT                   = tostring(var.app_port)
    FPM_MAX_CHILDREN            = tostring(var.fpm_max_children)
    RUN_MIGRATIONS              = "true"
    LAB_FAULT_INJECTION_ENABLED = "false"
  }, var.extra_env) : "${k}=${v}"])

  cloud_init = <<-YAML
    #cloud-config
    write_files:
      - path: /etc/ticketing/app.env
        permissions: "0644"
        content: |
          ${indent(6, local.env_file)}
      - path: /etc/systemd/system/ticketing.service
        permissions: "0644"
        content: |
          [Unit]
          Description=Ticketing API container
          Wants=network-online.target gcr-online.target docker.socket
          After=network-online.target gcr-online.target docker.socket

          [Service]
          Environment=HOME=/home/ticketing
          ExecStartPre=/bin/mkdir -p /home/ticketing
          # COS drops inbound traffic by default; open only the application port (VPC firewall still applies).
          ExecStartPre=/bin/sh -c '/sbin/iptables -w -C INPUT -p tcp --dport ${var.app_port} -j ACCEPT 2>/dev/null || /sbin/iptables -w -A INPUT -p tcp --dport ${var.app_port} -j ACCEPT'
          ExecStartPre=/usr/bin/docker-credential-gcr configure-docker --registries ${local.registry_host}
          ExecStartPre=-/usr/bin/docker rm -f ticketing
          ExecStartPre=/usr/bin/docker pull ${var.image}
          ExecStart=/usr/bin/docker run --rm --name ticketing --network host --env-file /etc/ticketing/app.env --ulimit nofile=65536:65536 ${var.image}
          ExecStop=/usr/bin/docker stop -t 20 ticketing
          Restart=always
          RestartSec=5

          [Install]
          WantedBy=multi-user.target
    runcmd:
      - systemctl daemon-reload
      - systemctl enable --now ticketing.service
  YAML
}

resource "google_compute_instance_template" "app" {
  name_prefix  = "${var.prefix}-app-"
  machine_type = var.machine_type
  tags         = [var.app_tag]
  labels       = merge(var.labels, { "lab-role" = "app" })

  disk {
    source_image = "cos-cloud/cos-stable"
    disk_type    = "pd-balanced"
    disk_size_gb = 10
    auto_delete  = true
    boot         = true
  }

  network_interface {
    subnetwork = var.subnet_id
    # No access_config: application VMs have no external IP.
  }

  metadata = {
    user-data                 = local.cloud_init
    google-logging-enabled    = "true"
    google-monitoring-enabled = "true"
    block-project-ssh-keys    = "true"
    enable-oslogin            = "TRUE"
  }

  service_account {
    email  = var.service_account_email
    scopes = ["cloud-platform"]
  }

  shielded_instance_config {
    enable_secure_boot          = true
    enable_vtpm                 = true
    enable_integrity_monitoring = true
  }

  scheduling {
    automatic_restart   = true
    on_host_maintenance = "MIGRATE"
  }

  lifecycle {
    create_before_destroy = true
  }
}

# Readiness (DB reachable): used by the load balancer to route traffic.
resource "google_compute_health_check" "ready" {
  name                = "${var.prefix}-app-ready"
  check_interval_sec  = 5
  timeout_sec         = 4
  healthy_threshold   = 2
  unhealthy_threshold = 3
  http_health_check {
    port         = var.app_port
    request_path = "/health"
  }
  log_config {
    enable = false
  }
}

# Liveness (process up, independent of the database): used by MIG autohealing so a database
# outage never causes a VM recreation loop.
resource "google_compute_health_check" "live" {
  name                = "${var.prefix}-app-live"
  check_interval_sec  = 10
  timeout_sec         = 5
  healthy_threshold   = 2
  unhealthy_threshold = 6
  http_health_check {
    port         = var.app_port
    request_path = "/health/live"
  }
}

resource "google_compute_instance_group_manager" "app" {
  name               = "${var.prefix}-app-mig"
  zone               = var.zone
  base_instance_name = "${var.prefix}-app"
  target_size        = var.initial_instances

  version {
    instance_template = google_compute_instance_template.app.id
  }

  named_port {
    name = "http"
    port = var.app_port
  }

  auto_healing_policies {
    health_check      = google_compute_health_check.live.id
    initial_delay_sec = 300
  }

  # New image or template → replace VMs one at a time without dropping below capacity.
  update_policy {
    type                  = "PROACTIVE"
    minimal_action        = "REPLACE"
    max_surge_fixed       = 1
    max_unavailable_fixed = 0
    replacement_method    = "SUBSTITUTE"
  }

  lifecycle {
    # The autoscaler owns the group size after creation.
    ignore_changes = [target_size]
  }
}

# Real CPU-driven autoscaling. The dashboard may temporarily override these values through the
# Compute API during an experiment; `terraform apply` (or the dashboard's Reset) restores them.
resource "google_compute_autoscaler" "app" {
  name   = "${var.prefix}-app-autoscaler"
  zone   = var.zone
  target = google_compute_instance_group_manager.app.id

  autoscaling_policy {
    min_replicas         = var.min_instances
    max_replicas         = var.max_instances
    cooldown_period      = var.instance_init_seconds
    stabilization_period = var.scale_in_stabilization_seconds
    mode                 = "ON"

    cpu_utilization {
      target = var.target_cpu_utilization
    }
  }
}
