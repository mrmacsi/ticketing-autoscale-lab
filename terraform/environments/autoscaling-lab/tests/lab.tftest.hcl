# Offline plan-time checks with mocked providers: `terraform test` (no cloud access, no cost).

mock_provider "google" {}
mock_provider "random" {}

variables {
  project_id     = "test-project"
  project_number = "123456789"
  allowed_emails = ["owner@example.com"]
  app_image      = "europe-west2-docker.pkg.dev/test-project/ticketing-scale-lab/ticketing-api:v1"
  loadgen_image  = "europe-west2-docker.pkg.dev/test-project/ticketing-scale-lab/load-generator:v1"
  control_image  = "europe-west2-docker.pkg.dev/test-project/ticketing-scale-lab/control-api:v1"
  queue_image    = "europe-west2-docker.pkg.dev/test-project/ticketing-scale-lab/waiting-room:v1"
}

run "autoscaler_uses_cpu_target_and_bounds" {
  command = plan

  assert {
    condition     = module.application[0].autoscaler_name == "ticketing-scale-lab-app-autoscaler"
    error_message = "unexpected autoscaler name"
  }
}

run "mig_and_autoscaler_configuration" {
  command = apply

  assert {
    condition     = output.mig_name == "ticketing-scale-lab-app-mig"
    error_message = "MIG name changed"
  }
}

run "approved_maximum_is_enforced" {
  command = plan
  variables {
    max_instances          = 6
    approved_max_instances = 5
  }
  expect_failures = [check.instance_limits]
}

run "foundation_only_stage_creates_no_workloads" {
  command = plan
  variables {
    enable_workloads = false
  }
  assert {
    condition     = output.application_url == null && output.dashboard_url == null && output.mig_name == null
    error_message = "workloads must not be created in the foundation stage"
  }
}

run "waiting_room_sits_behind_the_load_balancer_without_public_ip" {
  command = plan

  assert {
    condition     = length(module.waiting_room) == 1
    error_message = "waiting room must be deployed when its image is set"
  }
  assert {
    condition     = module.waiting_room[0].mig_name == "ticketing-scale-lab-queue-mig" && output.waiting_room != null
    error_message = "waiting room output missing"
  }
}

run "waiting_room_is_highly_available_by_default" {
  command = apply

  assert {
    condition     = module.waiting_room[0].store == "redis" && module.waiting_room[0].summary.replicas == 2
    error_message = "default waiting room must be 2 queue VMs on the redis store"
  }
  assert {
    condition     = module.waiting_room[0].summary.max_surge == 1 && module.waiting_room[0].summary.max_unavailable == 0
    error_message = "rolling updates must add a VM before removing one"
  }
  assert {
    condition     = module.waiting_room[0].summary.autohealing_path == "/queue/health"
    error_message = "autohealing must check /queue/health"
  }
  assert {
    condition = (module.waiting_room[0].summary.redis_tier == "BASIC" && module.waiting_room[0].summary.redis_memory_gb == 1
    && module.waiting_room[0].summary.redis_connect_mode == "DIRECT_PEERING" && module.waiting_room[0].summary.redis_eviction == "noeviction")
    error_message = "Memorystore must be BASIC 1 GB, direct peering, no eviction"
  }
  assert {
    condition     = module.waiting_room[0].summary.env_store == "redis" && module.waiting_room[0].summary.env_has_redis_addr
    error_message = "queue VMs must get STORE=redis and REDIS_ADDR"
  }
  assert {
    condition     = module.waiting_room[0].summary.template_has_image && !module.waiting_room[0].summary.template_has_watcher
    error_message = "the image must come from the instance template (no metadata image watcher)"
  }
  assert {
    condition     = module.waiting_room[0].summary.public_balancing_mode == "RATE" && module.waiting_room[0].summary.admin_scheme == "INTERNAL"
    error_message = "public backend must use RATE (shared with the internal admin load balancer)"
  }
  assert {
    condition     = output.waiting_room.private_url == "http://${module.waiting_room[0].private_ip}:8080" && output.waiting_room.redis_instance == "ticketing-scale-lab-queue-redis"
    error_message = "control plane must reach /admin/* on the internal load balancer's fixed IP"
  }
  assert {
    condition     = contains(keys(google_project_service.apis), "redis.googleapis.com")
    error_message = "redis.googleapis.com must be enabled for Memorystore"
  }
}

run "waiting_room_memory_fallback_is_one_vm_without_redis" {
  command = plan
  variables {
    waiting_room_store = "memory"
    queue_replicas     = 3
  }
  assert {
    condition     = module.waiting_room[0].replicas == 1 && module.waiting_room[0].redis_instance == null
    error_message = "memory store must run exactly one VM and no Memorystore"
  }
  assert {
    condition     = !contains(keys(google_project_service.apis), "redis.googleapis.com")
    error_message = "redis API must not be enabled without the redis store"
  }
}

run "no_waiting_room_without_image" {
  command = plan
  variables {
    queue_image = ""
  }
  assert {
    condition     = length(module.waiting_room) == 0 && output.waiting_room == null
    error_message = "waiting room must be optional"
  }
}
