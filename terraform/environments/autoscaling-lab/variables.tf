variable "project_id" {
  type = string
}

variable "project_number" {
  type        = string
  description = "Used to scope Secret Manager IAM conditions (secret resource names use the number)."
}

variable "region" {
  type    = string
  default = "europe-west2"
}

variable "zone" {
  type    = string
  default = "europe-west2-b"
}

variable "prefix" {
  type    = string
  default = "ticketing-scale-lab"
}

variable "short_prefix" {
  type        = string
  default     = "tsl"
  description = "Prefix for service-account IDs (30-character limit)."
}

variable "enable_workloads" {
  type        = bool
  default     = true
  description = "false creates only the foundation (APIs, network, registry, secrets, database) so images can be pushed first."
}

# --- images (set by scripts/build-and-push.sh in images.auto.tfvars.json) -------------------

variable "app_image" {
  type    = string
  default = ""
}

variable "loadgen_image" {
  type    = string
  default = ""
}

variable "control_image" {
  type    = string
  default = ""
}

variable "queue_image" {
  type        = string
  default     = ""
  description = "Waiting-room image; empty means no waiting room is deployed."
}

# --- application tier -------------------------------------------------------------------

variable "vm_machine_type" {
  type    = string
  default = "n1-standard-1"
}

variable "initial_instances" {
  type    = number
  default = 1
}

variable "min_instances" {
  type    = number
  default = 1
}

variable "max_instances" {
  type    = number
  default = 5
}

variable "approved_max_instances" {
  type        = number
  default     = 5
  description = "Hard ceiling approved for spending. The autoscaler and the dashboard can never exceed it."
}

variable "target_cpu_utilization" {
  type    = number
  default = 0.6
  validation {
    condition     = var.target_cpu_utilization > 0.05 && var.target_cpu_utilization <= 0.95
    error_message = "target_cpu_utilization must be between 0.05 and 0.95."
  }
}

variable "instance_init_seconds" {
  type    = number
  default = 60
}

variable "scale_in_stabilization" {
  type        = number
  default     = 240
  description = "Autoscaler stabilization period (seconds) before scale-in decisions."
}

variable "fpm_max_children" {
  type        = number
  default     = 32
  description = "PHP-FPM workers per VM; each holds one database connection."
}

variable "app_port" {
  type    = number
  default = 8080
}

# --- database ---------------------------------------------------------------------------

variable "db_tier" {
  type    = string
  default = "db-custom-1-3840"
}

variable "db_max_connections" {
  type    = number
  default = 200
}

# --- load generator ---------------------------------------------------------------------

variable "generator_machine_type" {
  type    = string
  default = "e2-small"
}

variable "generator_max_rate" {
  type    = number
  default = 500
}

variable "generator_max_vus" {
  type        = number
  default     = 1000000
  description = "Customers in flight, including those waiting in the waiting room (effectively no cap; the generator VM's memory is the real limit)."
}

variable "max_experiment_seconds" {
  type    = number
  default = 1800
}

# --- control plane ----------------------------------------------------------------------

variable "allowed_emails" {
  type        = list(string)
  description = "Google accounts allowed to use the dashboard (Cloud Run invoker + application allowlist)."
}

variable "auth_mode" {
  type    = string
  default = "iam_proxy"
  validation {
    condition     = contains(["iam_proxy", "google_signin"], var.auth_mode)
    error_message = "auth_mode must be iam_proxy or google_signin."
  }
}

variable "google_client_id" {
  type        = string
  default     = ""
  description = "OAuth web client ID, only for auth_mode = google_signin."
}

variable "control_min_instances" {
  type        = number
  default     = 1
  description = "1 keeps telemetry collection running without a browser open (billed continuously)."
}

# --- cost controls ----------------------------------------------------------------------

variable "billing_account_id" {
  type        = string
  default     = ""
  description = "Billing account for the budget alert (e.g. XXXXXX-XXXXXX-XXXXXX). Empty disables the budget."
}

variable "budget_amount" {
  type        = number
  default     = 0
  description = "Monthly budget alert amount in the billing account's currency. 0 disables it. An alert, not a cap."
}

variable "lb_log_sample_rate" {
  type    = number
  default = 0.05
}

# --- waiting room -----------------------------------------------------------------------

variable "queue_machine_type" {
  type    = string
  default = "e2-small"
}

variable "queue_port" {
  type    = number
  default = 8080
}
