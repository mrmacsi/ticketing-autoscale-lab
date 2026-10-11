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
variable "network_id" {
  type        = string
  description = "Lab VPC (Memorystore authorized network and the internal admin load balancer)."
}
variable "store" {
  type        = string
  default     = "redis"
  description = "redis: var.replicas queue VMs share Memorystore (highly available). memory: one VM, line in memory."
  validation {
    condition     = contains(["redis", "memory"], var.store)
    error_message = "store must be redis or memory."
  }
}
variable "replicas" {
  type        = number
  default     = 2
  description = "Queue VMs with the redis store (the memory store always runs exactly one)."
  validation {
    condition     = var.replicas >= 1 && var.replicas <= 5
    error_message = "replicas must be 1..5."
  }
}
variable "redis_tier" {
  type    = string
  default = "BASIC"
  validation {
    condition     = contains(["BASIC", "STANDARD_HA"], var.redis_tier)
    error_message = "redis_tier must be BASIC or STANDARD_HA."
  }
}
variable "redis_memory_gb" {
  type    = number
  default = 1
}
variable "redis_reserved_ip_range" {
  type        = string
  default     = "10.60.32.0/29"
  description = "/29 for the Memorystore instance (outside the lab subnets 10.60.0.0/20 and 10.60.16.0/24)."
}
