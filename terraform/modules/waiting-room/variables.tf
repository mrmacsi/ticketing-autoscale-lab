variable "prefix" { type = string }
variable "project_id" { type = string }
variable "region" { type = string }
variable "zone" { type = string }
variable "subnet_id" { type = string }
variable "image" { type = string }
variable "machine_type" { type = string }
variable "queue_tag" { type = string }
variable "port" { type = number }
variable "app_port" { type = number }
variable "labels" { type = map(string) }
variable "admin_token_secret" {
  type        = string
  description = "Secret Manager resource name of the lab admin token (signs admission tokens and guards /admin/*)."
}
variable "app_backend_service_name" {
  type        = string
  description = "Backend service whose health decides how many app servers the waiting room admits for."
}
variable "app_instance_group" {
  type        = string
  description = "Instance group URL of the application MIG (getHealth needs it)."
}
variable "service_account_email" { type = string }
variable "log_sample_rate" { type = number }
