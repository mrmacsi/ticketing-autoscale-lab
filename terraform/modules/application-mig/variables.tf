variable "prefix" { type = string }
variable "zone" { type = string }
variable "subnet_id" { type = string }
variable "image" { type = string }
variable "machine_type" { type = string }
variable "app_tag" { type = string }
variable "app_port" { type = number }
variable "labels" { type = map(string) }
variable "initial_instances" { type = number }
variable "min_instances" { type = number }
variable "max_instances" { type = number }
variable "target_cpu_utilization" { type = number }
variable "instance_init_seconds" { type = number }
variable "scale_in_stabilization_seconds" { type = number }
variable "fpm_max_children" { type = number }
variable "db_host" { type = string }
variable "db_name" { type = string }
variable "db_user" { type = string }
variable "db_password_secret" { type = string }
variable "admin_token_secret" { type = string }
variable "app_key_secret" { type = string }
variable "admin_allow_cidrs" { type = list(string) }
variable "extra_env" {
  type    = map(string)
  default = {}
}
variable "service_account_email" { type = string }
variable "load_allow_cidrs" {
  type        = list(string)
  default     = []
  description = "Networks allowed to read /lab/load and /lab/cpu (the waiting room's load signals)."
}
