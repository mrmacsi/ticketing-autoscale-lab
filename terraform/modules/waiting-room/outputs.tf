output "backend_service_id" { value = google_compute_backend_service.queue.id }
output "private_ip" { value = google_compute_address.queue.address }
# Internal load balancer in front of every queue VM (control plane: /admin/*).
output "private_url" { value = "http://${google_compute_address.queue.address}:${var.port}" }
output "mig_name" { value = google_compute_instance_group_manager.queue.name }
output "instance_group" { value = google_compute_instance_group_manager.queue.instance_group }
output "replicas" { value = local.replicas }
output "store" { value = var.store }
output "redis_instance" { value = local.redis ? google_redis_instance.queue[0].name : null }
output "redis_address" {
  value = local.redis ? "${google_redis_instance.queue[0].host}:${google_redis_instance.queue[0].port}" : null
}
# Settings that decide availability (asserted by the root module's terraform tests).
output "summary" {
  value = {
    replicas              = google_compute_instance_group_manager.queue.target_size
    max_surge             = google_compute_instance_group_manager.queue.update_policy[0].max_surge_fixed
    max_unavailable       = google_compute_instance_group_manager.queue.update_policy[0].max_unavailable_fixed
    autohealing_path      = google_compute_health_check.queue_live.http_health_check[0].request_path
    public_balancing_mode = one([for b in google_compute_backend_service.queue.backend : b.balancing_mode])
    admin_scheme          = google_compute_forwarding_rule.queue_admin.load_balancing_scheme
    redis_tier            = try(google_redis_instance.queue[0].tier, null)
    redis_memory_gb       = try(google_redis_instance.queue[0].memory_size_gb, null)
    redis_connect_mode    = try(google_redis_instance.queue[0].connect_mode, null)
    redis_eviction        = try(google_redis_instance.queue[0].redis_configs["maxmemory-policy"], null)
    env_has_redis_addr    = strcontains(google_compute_instance_template.queue.metadata["user-data"], "REDIS_ADDR=")
    env_store             = regex("STORE=([a-z]+)", google_compute_instance_template.queue.metadata["user-data"])[0]
    template_has_image    = strcontains(google_compute_instance_template.queue.metadata["user-data"], "docker pull ${var.image}")
    template_has_watcher  = strcontains(google_compute_instance_template.queue.metadata["user-data"], "watch-image")
  }
}
