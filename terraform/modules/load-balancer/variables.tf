variable "prefix" { type = string }
variable "instance_group" { type = string }
variable "health_check_id" { type = string }
variable "log_sample_rate" { type = number }
variable "labels" { type = map(string) }
variable "waiting_room_backend_id" {
  type        = string
  default     = null
  description = "Backend service for /queue/* (the waiting room); null routes everything to the app."
}
