# Waiting room (queue servers), highly available: a managed instance group of `replicas` small COS
# VMs running the queue container, all sharing one Memorystore for Redis instance (the line, counters
# and config live there; the instances elect one leader that admits). See docs/waiting-room.md,
# "High availability".
#
#   customers  --/queue/*-->  external Application LB (backend service below, RATE mode)  --> any VM
#   control plane --/admin/*--> internal passthrough NLB on a fixed private IP              --> any VM
#
# store = "memory" is the fallback: one VM, no Memorystore, the line lives in that VM's memory.

locals {
  redis    = var.store == "redis"
  replicas = local.redis ? var.replicas : 1

  registry_host = split("/", var.image)[0]
  env_file = join("\n", [for k, v in merge({
    PORT                   = tostring(var.port)
    APP_PORT               = tostring(var.app_port)
    LAB_ADMIN_TOKEN_SECRET = var.admin_token_secret
    CAPACITY_SOURCE        = "gce"
    GCP_PROJECT_ID         = var.project_id
    BACKEND_SERVICE_NAME   = var.app_backend_service_name
    INSTANCE_GROUP_URL     = var.app_instance_group
    STORE                  = var.store
    }, local.redis ? {
    REDIS_ADDR = "${google_redis_instance.queue[0].host}:${google_redis_instance.queue[0].port}"
  } : {}) : "${k}=${v}"])

  # The image is part of the instance template: a new image is a new template, and the MIG replaces
  # the VMs one at a time (surge 1, never fewer than `replicas` serving). With the redis store nobody
  # loses their place while that happens.
  cloud_init = <<-YAML
    #cloud-config
    write_files:
      - path: /etc/waiting-room/queue.env
        permissions: "0644"
        content: |
          ${indent(6, local.env_file)}
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
          ExecStartPre=/usr/bin/docker-credential-gcr configure-docker --registries ${local.registry_host}
          ExecStartPre=-/usr/bin/docker rm -f waiting-room
          ExecStartPre=/usr/bin/docker pull ${var.image}
          ExecStart=/usr/bin/docker run --rm --name waiting-room --network host --env-file /etc/waiting-room/queue.env --ulimit nofile=1048576:1048576 ${var.image}
          # SIGTERM: the queue answers open long polls and hands leadership over before exiting.
          ExecStop=/usr/bin/docker stop -t 10 waiting-room
          Restart=always
          RestartSec=5

          [Install]
          WantedBy=multi-user.target
    runcmd:
      - sysctl -w net.core.somaxconn=8192
      - systemctl daemon-reload
      - systemctl enable --now waiting-room.service
  YAML
}

# --- shared state -----------------------------------------------------------------------------

# Memorystore for Redis, BASIC tier by default (no replica: a Redis failure loses the line, like a
# single queue VM did before; STANDARD_HA adds a replica with automatic failover). DIRECT_PEERING
# because the deploying account cannot create Private Service Access peering
# (servicenetworking.services.addPeering); Memorystore creates its own peering to the lab VPC.
# No firewall rule is needed: VPC egress is allowed and the Redis side is Google-managed.
resource "google_redis_instance" "queue" {
  count              = local.redis ? 1 : 0
  name               = "${var.prefix}-queue-redis"
  display_name       = "Ticketing lab waiting room"
  tier               = var.redis_tier
  memory_size_gb     = var.redis_memory_gb
  region             = var.region
  location_id        = var.zone
  authorized_network = var.network_id
  connect_mode       = "DIRECT_PEERING"
  reserved_ip_range  = var.redis_reserved_ip_range
  redis_version      = "REDIS_7_2"
  labels             = merge(var.labels, { "lab-role" = "queue" })
  redis_configs = {
    # Never evict: the leader lease is the only key with a TTL and must not be dropped under memory
    # pressure; a full Redis should fail writes loudly instead.
    "maxmemory-policy" = "noeviction"
  }
}

# --- queue VMs ----------------------------------------------------------------------------------

resource "google_compute_instance_template" "queue" {
  name_prefix  = "${var.prefix}-queue-"
  machine_type = var.machine_type
  tags         = [var.queue_tag]
  labels       = merge(var.labels, { "lab-role" = "queue" })

  disk {
    source_image = "cos-cloud/cos-stable"
    disk_type    = "pd-balanced"
    disk_size_gb = 10
    auto_delete  = true
    boot         = true
  }

  network_interface {
    subnetwork = var.subnet_id
    # No access_config: reached only through the load balancers and the VPC.
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

# Serving check (load balancers) and autohealing check (recreates a VM whose queue stopped answering).
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

resource "google_compute_health_check" "queue_live" {
  name                = "${var.prefix}-queue-live"
  check_interval_sec  = 10
  timeout_sec         = 5
  healthy_threshold   = 2
  unhealthy_threshold = 6
  http_health_check {
    port         = var.port
    request_path = "/queue/health"
  }
}

resource "google_compute_instance_group_manager" "queue" {
  name               = "${var.prefix}-queue-mig"
  zone               = var.zone
  base_instance_name = "${var.prefix}-queue"
  target_size        = local.replicas

  version {
    instance_template = google_compute_instance_template.queue.id
  }

  named_port {
    name = "http"
    port = var.port
  }

  auto_healing_policies {
    health_check      = google_compute_health_check.queue_live.id
    initial_delay_sec = 180
  }

  update_policy {
    type                  = "PROACTIVE"
    minimal_action        = "REPLACE"
    replacement_method    = "SUBSTITUTE"
    max_surge_fixed       = 1
    max_unavailable_fixed = 0
  }

  # Wait until the VMs are running so the backends below attach to a populated group.
  wait_for_instances        = true
  wait_for_instances_status = "STABLE"

  timeouts {
    create = "15m"
    update = "15m"
  }
}

# --- public path: /queue/* through the external Application Load Balancer -----------------------

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
    group = google_compute_instance_group_manager.queue.instance_group
    # RATE, not UTILIZATION: the same instance group also backs the internal passthrough load
    # balancer below (CONNECTION mode), and UTILIZATION cannot be combined with any other mode.
    balancing_mode        = "RATE"
    max_rate_per_instance = 10000
    capacity_scaler       = 1.0
  }

  log_config {
    enable      = true
    sample_rate = var.log_sample_rate
  }
}

# --- private path: /admin/* for the control plane on a fixed private IP --------------------------

# Fixed private address so the control plane can reach /admin/* without discovery (it is now the
# internal load balancer's address, so any healthy queue VM answers).
resource "google_compute_address" "queue" {
  name         = "${var.prefix}-queue-ip"
  region       = var.region
  subnetwork   = var.subnet_id
  address_type = "INTERNAL"
  labels       = var.labels
}

resource "google_compute_region_backend_service" "queue_admin" {
  name                            = "${var.prefix}-queue-admin"
  region                          = var.region
  load_balancing_scheme           = "INTERNAL"
  protocol                        = "TCP"
  health_checks                   = [google_compute_health_check.queue.id]
  connection_draining_timeout_sec = 10

  backend {
    group          = google_compute_instance_group_manager.queue.instance_group
    balancing_mode = "CONNECTION"
  }
}

resource "google_compute_forwarding_rule" "queue_admin" {
  name                  = "${var.prefix}-queue-admin"
  region                = var.region
  load_balancing_scheme = "INTERNAL"
  ip_protocol           = "TCP"
  ip_address            = google_compute_address.queue.address
  ports                 = [tostring(var.port)]
  network               = var.network_id
  subnetwork            = var.subnet_id
  backend_service       = google_compute_region_backend_service.queue_admin.id
  labels                = var.labels
}
