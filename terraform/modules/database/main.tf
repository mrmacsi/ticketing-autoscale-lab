# Cloud SQL for PostgreSQL with no public IP. Clients reach it through a Private Service Connect
# endpoint inside the lab VPC (an internal IP), with TLS required.

resource "google_sql_database_instance" "main" {
  name                = "${var.prefix}-pg"
  database_version    = "POSTGRES_16"
  region              = var.region
  deletion_protection = false
  deletion_policy     = "DELETE"

  settings {
    tier              = var.tier
    edition           = "ENTERPRISE"
    availability_type = "ZONAL"
    disk_type         = "PD_SSD"
    disk_size         = 10
    disk_autoresize   = false
    user_labels       = var.labels

    location_preference {
      zone = var.zone
    }

    ip_configuration {
      ipv4_enabled = false
      ssl_mode     = "ENCRYPTED_ONLY"
      psc_config {
        psc_enabled               = true
        allowed_consumer_projects = [var.project_id]
      }
    }

    backup_configuration {
      enabled = false
    }

    database_flags {
      name  = "max_connections"
      value = tostring(var.max_connections)
    }

    insights_config {
      query_insights_enabled = false
    }
  }
}

resource "google_sql_database" "ticketing" {
  name     = var.database_name
  instance = google_sql_database_instance.main.name
  # The dedicated instance owns this database. Full teardown deletes the instance,
  # which removes active sessions and all objects without a separate DROP DATABASE.
  deletion_policy = "ABANDON"
}

resource "random_password" "app" {
  length  = 32
  special = false
}

resource "random_password" "observer" {
  length  = 32
  special = false
}

resource "google_sql_user" "app" {
  name     = "ticketing_app"
  instance = google_sql_database_instance.main.name
  password = random_password.app.result
  # PostgreSQL cannot drop roles that own objects or have dependent grants.
  # These roles disappear with the dedicated instance during full teardown.
  deletion_policy = "ABANDON"
}

# Read-only role for the control plane; the application's migration grants SELECT to it.
resource "google_sql_user" "observer" {
  name            = "lab_observer"
  instance        = google_sql_database_instance.main.name
  password        = random_password.observer.result
  deletion_policy = "ABANDON"
}

resource "google_compute_address" "psc" {
  name         = "${var.prefix}-pg-psc"
  region       = var.region
  subnetwork   = var.subnet_id
  address_type = "INTERNAL"
}

resource "google_compute_forwarding_rule" "psc" {
  name                  = "${var.prefix}-pg-psc"
  region                = var.region
  network               = var.network_id
  ip_address            = google_compute_address.psc.id
  target                = google_sql_database_instance.main.psc_service_attachment_link
  load_balancing_scheme = ""
}
