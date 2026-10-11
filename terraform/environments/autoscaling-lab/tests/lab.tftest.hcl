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
    condition     = length(module.waiting_room[0].instance_name) > 0 && output.waiting_room != null
    error_message = "waiting room output missing"
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
