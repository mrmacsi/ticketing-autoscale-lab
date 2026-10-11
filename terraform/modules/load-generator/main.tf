# Separate VM that generates synthetic customers. It never runs on an application VM, has no
# inbound ports (except IAP SSH), and can only target the URL Terraform gives it.

locals {
  registry_host = split("/", var.image)[0]
  env_file = join("\n", [for k, v in {
    TARGET_BASE_URL      = var.target_base_url
    ALLOWED_TARGET_HOSTS = var.allowed_target_host
    GOOGLE_CLOUD_PROJECT = var.project_id
    FIRESTORE_DATABASE   = var.firestore_database
    MAX_RATE             = tostring(var.max_rate)
    MAX_VUS              = tostring(var.max_vus)
    MAX_DURATION_SECONDS = tostring(var.max_duration_seconds)
  } : "${k}=${v}"])

  cloud_init = <<-YAML
    #cloud-config
    write_files:
      - path: /etc/loadgen/agent.env
        permissions: "0644"
        content: |
          ${indent(6, local.env_file)}
      - path: /etc/systemd/system/loadgen.service
        permissions: "0644"
        content: |
          [Unit]
          Description=Ticketing lab load generator agent
          Wants=network-online.target gcr-online.target docker.socket
          After=network-online.target gcr-online.target docker.socket

          [Service]
          Environment=HOME=/home/loadgen
          ExecStartPre=/bin/mkdir -p /home/loadgen
          ExecStartPre=/usr/bin/docker-credential-gcr configure-docker --registries ${local.registry_host}
          ExecStartPre=-/usr/bin/docker rm -f loadgen
          ExecStartPre=/usr/bin/docker pull ${var.image}
          ExecStart=/usr/bin/docker run --rm --name loadgen --env-file /etc/loadgen/agent.env --ulimit nofile=1048576:1048576 ${var.image} agent
          ExecStop=/usr/bin/docker stop -t 10 loadgen
          Restart=always
          RestartSec=5

          [Install]
          WantedBy=multi-user.target
    runcmd:
      - sysctl -w net.ipv4.ip_local_port_range="10240 65000"
      - systemctl daemon-reload
      - systemctl enable --now loadgen.service
  YAML
}

# The container image is pulled by a systemd unit that cloud-init writes at boot, so changing the
# image or environment only in metadata would leave the old container running. Replace the VM when either changes.
resource "terraform_data" "image" {
  input = sha256(local.cloud_init)
}

resource "google_compute_instance" "loadgen" {
  name         = "${var.prefix}-loadgen"
  zone         = var.zone
  machine_type = var.machine_type
  tags         = [var.loadgen_tag]
  labels       = merge(var.labels, { "lab-role" = "loadgen" })

  boot_disk {
    initialize_params {
      image = "cos-cloud/cos-stable"
      type  = "pd-balanced"
      size  = 10
    }
  }

  network_interface {
    subnetwork = var.subnet_id
    # Ephemeral external IP for egress to the load balancer's public address. No ingress is
    # allowed by the firewall except IAP SSH.
    access_config {}
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

  allow_stopping_for_update = true

  lifecycle {
    replace_triggered_by = [terraform_data.image]
  }
}
