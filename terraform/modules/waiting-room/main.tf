# Waiting room (queue server): one small VM with an in-memory FIFO queue, behind the same external
# load balancer as the application (URL map sends /queue/* here). It admits customers only as fast as
# the app servers that the load balancer reports HEALTHY can take them. See docs/waiting-room.md.

locals {
  registry_host = split("/", var.image)[0]
  env_file = join("\n", [for k, v in {
    PORT                   = tostring(var.port)
    APP_PORT               = tostring(var.app_port)
    LAB_ADMIN_TOKEN_SECRET = var.admin_token_secret
    CAPACITY_SOURCE        = "gce"
    GCP_PROJECT_ID         = var.project_id
    BACKEND_SERVICE_NAME   = var.app_backend_service_name
    INSTANCE_GROUP_URL     = var.app_instance_group
  } : "${k}=${v}"])

  # The image is read from the instance metadata attribute "container-image" at every start, and a
  # watcher restarts the container when that attribute changes. A new image therefore updates the
  # running VM in place: replacing the VM would drop it from the load balancer's instance group
  # (measured: queue backend empty and /queue/* 503 until a second terraform apply re-added it).
  cloud_init = <<-YAML
    #cloud-config
    write_files:
      - path: /etc/waiting-room/queue.env
        permissions: "0644"
        content: |
          ${indent(6, local.env_file)}
      - path: /etc/waiting-room/run.sh
        permissions: "0644"
        content: |
          set -eu
          image="$(curl -fsS -H 'Metadata-Flavor: Google' http://metadata.google.internal/computeMetadata/v1/instance/attributes/container-image)"
          /usr/bin/docker-credential-gcr configure-docker --registries ${local.registry_host}
          /usr/bin/docker rm -f waiting-room >/dev/null 2>&1 || true
          /usr/bin/docker pull "$image"
          exec /usr/bin/docker run --rm --name waiting-room --network host --env-file /etc/waiting-room/queue.env --ulimit nofile=1048576:1048576 "$image"
      - path: /etc/waiting-room/watch-image.sh
        permissions: "0644"
        content: |
          url=http://metadata.google.internal/computeMetadata/v1/instance/attributes/container-image
          while true; do
            want="$(curl -fsS -H 'Metadata-Flavor: Google' "$url" || true)"
            have="$(/usr/bin/docker inspect -f '{{.Config.Image}}' waiting-room 2>/dev/null || true)"
            if [ -n "$want" ] && [ -n "$have" ] && [ "$want" != "$have" ]; then
              echo "container image changed: $have -> $want; restarting"
              systemctl restart waiting-room.service
            fi
            # Long poll: returns when the attribute changes, or after 5 minutes.
            curl -fsS -o /dev/null -H 'Metadata-Flavor: Google' "$url?wait_for_change=true&timeout_sec=300" || sleep 5
            sleep 2
          done
      - path: /etc/systemd/system/waiting-room.service
        permissions: "0644"
        content: |
          [Unit]
          Description=Ticketing lab waiting room
          Wants=network-online.target gcr-online.target docker.socket
          After=network-online.target gcr-online.target docker.socket

          [Service]
          Environment=HOME=/home/queue
          ExecStartPre=/bin/mkdir -p /home/queue
          # COS drops inbound traffic by default; open only the queue port (VPC firewall still applies).
          ExecStartPre=/bin/sh -c '/sbin/iptables -w -C INPUT -p tcp --dport ${var.port} -j ACCEPT 2>/dev/null || /sbin/iptables -w -A INPUT -p tcp --dport ${var.port} -j ACCEPT'
          ExecStart=/bin/bash /etc/waiting-room/run.sh
          ExecStop=/usr/bin/docker stop -t 10 waiting-room
          Restart=always
          RestartSec=5

          [Install]
          WantedBy=multi-user.target
      - path: /etc/systemd/system/waiting-room-image-watch.service
        permissions: "0644"
        content: |
          [Unit]
          Description=Restart the waiting room when its container-image metadata changes
          After=waiting-room.service

          [Service]
          ExecStart=/bin/bash /etc/waiting-room/watch-image.sh
          Restart=always
          RestartSec=5

          [Install]
          WantedBy=multi-user.target
    runcmd:
      - sysctl -w net.core.somaxconn=8192
      - systemctl daemon-reload
      - systemctl enable --now waiting-room.service waiting-room-image-watch.service
  YAML
}

# Fixed private address so the control plane can reach /admin/* without discovery.
resource "google_compute_address" "queue" {
  name         = "${var.prefix}-queue-ip"
  region       = var.region
  subnetwork   = var.subnet_id
  address_type = "INTERNAL"
  labels       = var.labels
}

resource "google_compute_instance" "queue" {
  name         = "${var.prefix}-queue"
  zone         = var.zone
  machine_type = var.machine_type
  tags         = [var.queue_tag]
  labels       = merge(var.labels, { "lab-role" = "queue" })

  boot_disk {
    initialize_params {
      image = "cos-cloud/cos-stable"
      type  = "pd-balanced"
      size  = 10
    }
  }

  network_interface {
    subnetwork = var.subnet_id
    network_ip = google_compute_address.queue.address
    # No access_config: reached only through the load balancer and the VPC.
  }

  metadata = {
    user-data                 = local.cloud_init
    container-image           = var.image
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

  allow_stopping_for_update = true
}

resource "google_compute_instance_group" "queue" {
  name      = "${var.prefix}-queue-group"
  zone      = var.zone
  instances = [google_compute_instance.queue.self_link]
  named_port {
    name = "http"
    port = var.port
  }
}

resource "google_compute_health_check" "queue" {
  name                = "${var.prefix}-queue-health"
  check_interval_sec  = 5
  timeout_sec         = 4
  healthy_threshold   = 2
  unhealthy_threshold = 3
  http_health_check {
    port         = var.port
    request_path = "/queue/health"
  }
}

resource "google_compute_backend_service" "queue" {
  name                  = "${var.prefix}-queue-backend"
  load_balancing_scheme = "EXTERNAL_MANAGED"
  protocol              = "HTTP"
  port_name             = "http"
  # Long polls hold a request for up to 25 s.
  timeout_sec                     = 40
  connection_draining_timeout_sec = 10
  health_checks                   = [google_compute_health_check.queue.id]

  backend {
    group           = google_compute_instance_group.queue.self_link
    balancing_mode  = "UTILIZATION"
    max_utilization = 1.0
    capacity_scaler = 1.0
  }

  log_config {
    enable      = true
    sample_rate = var.log_sample_rate
  }
}
