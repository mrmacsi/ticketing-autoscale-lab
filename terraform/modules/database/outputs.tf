output "instance_name" { value = google_sql_database_instance.main.name }
output "connection_name" { value = google_sql_database_instance.main.connection_name }
output "host" {
  description = "Private Service Connect endpoint IP inside the lab VPC."
  value       = google_compute_address.psc.address
}
output "database_name" { value = google_sql_database.ticketing.name }
output "app_user" { value = google_sql_user.app.name }
output "observer_user" { value = google_sql_user.observer.name }
output "app_password" {
  value     = random_password.app.result
  sensitive = true
}
output "observer_password" {
  value     = random_password.observer.result
  sensitive = true
}
