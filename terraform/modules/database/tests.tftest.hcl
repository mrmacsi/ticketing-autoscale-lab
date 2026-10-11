mock_provider "google" {}
mock_provider "random" {}

variables {
  prefix          = "ticketing-scale-lab"
  project_id      = "p"
  region          = "europe-west2"
  zone            = "europe-west2-b"
  tier            = "db-custom-1-3840"
  max_connections = 200
  database_name   = "ticketing"
  network_id      = "projects/p/global/networks/n"
  subnet_id       = "projects/p/regions/europe-west2/subnetworks/s"
  labels          = {}
}

run "database_has_no_public_access" {
  command = plan

  assert {
    condition     = google_sql_database_instance.main.settings[0].ip_configuration[0].ipv4_enabled == false
    error_message = "Cloud SQL must not have a public IP"
  }
  assert {
    condition     = length(google_sql_database_instance.main.settings[0].ip_configuration[0].authorized_networks) == 0
    error_message = "no authorised networks allowed"
  }
  assert {
    condition     = google_sql_database_instance.main.settings[0].ip_configuration[0].ssl_mode == "ENCRYPTED_ONLY"
    error_message = "TLS must be required"
  }
  assert {
    condition     = google_sql_database.ticketing.deletion_policy == "ABANDON" && google_sql_user.app.deletion_policy == "ABANDON" && google_sql_user.observer.deletion_policy == "ABANDON"
    error_message = "Full teardown must delete the dedicated instance instead of dropping PostgreSQL databases or dependent roles separately"
  }
  assert {
    condition     = google_sql_database_instance.main.deletion_policy == "DELETE" && !google_sql_database_instance.main.deletion_protection
    error_message = "The dedicated SQL instance must actually be deleted during full teardown"
  }
}
