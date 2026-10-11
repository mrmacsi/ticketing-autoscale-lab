locals {
  labels = {
    lab        = var.prefix
    managed-by = "terraform"
    purpose    = "autoscaling-demo"
  }
  app_tag        = "${var.prefix}-app"
  loadgen_tag    = "${var.prefix}-loadgen"
  queue_tag      = "${var.prefix}-queue"
  waiting_room   = var.enable_workloads && var.queue_image != ""
  control_cidr   = "10.60.16.0/24"
  workloads_cidr = "10.60.0.0/20"
  registry       = "${var.region}-docker.pkg.dev/${var.project_id}/${google_artifact_registry_repository.images.repository_id}"
  firestore_db   = var.prefix
}

check "instance_limits" {
  assert {
    condition     = var.min_instances <= var.initial_instances && var.initial_instances <= var.max_instances && var.max_instances <= var.approved_max_instances
    error_message = "Require min_instances <= initial_instances <= max_instances <= approved_max_instances."
  }
}

resource "google_project_service" "apis" {
  for_each = toset(concat([
    "compute.googleapis.com",
    "sqladmin.googleapis.com",
    "run.googleapis.com",
    "artifactregistry.googleapis.com",
    "secretmanager.googleapis.com",
    "firestore.googleapis.com",
    "monitoring.googleapis.com",
    "logging.googleapis.com",
    "iam.googleapis.com",
    "iap.googleapis.com",
    "cloudresourcemanager.googleapis.com",
  ], var.billing_account_id != "" ? ["billingbudgets.googleapis.com"] : []))
  service = each.value
  # Never disable shared project APIs on destroy; other workloads in the project may use them.
  disable_on_destroy = false
}

resource "google_artifact_registry_repository" "images" {
  repository_id = var.prefix
  location      = var.region
  format        = "DOCKER"
  description   = "Images for the ticketing autoscaling lab"
  depends_on    = [google_project_service.apis]

  cleanup_policies {
    id     = "keep-recent"
    action = "KEEP"
    most_recent_versions {
      keep_count = 10
    }
  }
}

module "networking" {
  source         = "../../modules/networking"
  prefix         = var.prefix
  region         = var.region
  workloads_cidr = local.workloads_cidr
  control_cidr   = local.control_cidr
  app_tag        = local.app_tag
  loadgen_tag    = local.loadgen_tag
  queue_tag      = local.queue_tag
  app_port       = var.app_port
  queue_port     = var.queue_port
  depends_on     = [google_project_service.apis]
}

module "database" {
  source          = "../../modules/database"
  prefix          = var.prefix
  project_id      = var.project_id
  region          = var.region
  zone            = var.zone
  tier            = var.db_tier
  max_connections = var.db_max_connections
  database_name   = "ticketing"
  network_id      = module.networking.network_id
  subnet_id       = module.networking.workloads_subnet_id
  labels          = local.labels
  depends_on      = [google_project_service.apis]
}

# --- secrets (generated here, stored in Secret Manager, read by workloads at runtime) ---------

resource "random_password" "admin_token" {
  length  = 48
  special = false
}

resource "random_bytes" "app_key" {
  length = 32
}

locals {
  secrets = {
    "db-app-password"      = module.database.app_password
    "db-observer-password" = module.database.observer_password
    "admin-token"          = random_password.admin_token.result
    "app-key"              = "base64:${random_bytes.app_key.base64}"
  }
}

resource "google_secret_manager_secret" "lab" {
  for_each  = toset(keys(local.secrets))
  secret_id = "${var.prefix}-${each.key}"
  replication {
    auto {}
  }
  depends_on = [google_project_service.apis]
}

resource "google_secret_manager_secret_version" "lab" {
  for_each    = toset(keys(local.secrets))
  secret      = google_secret_manager_secret.lab[each.key].id
  secret_data = local.secrets[each.key]
}

# --- experiment history and generator command channel ------------------------------------

resource "google_firestore_database" "lab" {
  name                    = local.firestore_db
  location_id             = var.region
  type                    = "FIRESTORE_NATIVE"
  delete_protection_state = "DELETE_PROTECTION_DISABLED"
  deletion_policy         = "DELETE"
  depends_on              = [google_project_service.apis]
}

# --- workloads ----------------------------------------------------------------------------

module "application" {
  count  = var.enable_workloads ? 1 : 0
  source = "../../modules/application-mig"

  prefix                         = var.prefix
  zone                           = var.zone
  subnet_id                      = module.networking.workloads_subnet_id
  image                          = var.app_image
  machine_type                   = var.vm_machine_type
  app_tag                        = local.app_tag
  app_port                       = var.app_port
  labels                         = local.labels
  initial_instances              = var.initial_instances
  min_instances                  = var.min_instances
  max_instances                  = var.max_instances
  target_cpu_utilization         = var.target_cpu_utilization
  instance_init_seconds          = var.instance_init_seconds
  scale_in_stabilization_seconds = var.scale_in_stabilization
  fpm_max_children               = var.fpm_max_children
  db_host                        = module.database.host
  db_name                        = module.database.database_name
  db_user                        = module.database.app_user
  db_password_secret             = google_secret_manager_secret.lab["db-app-password"].id
  admin_token_secret             = google_secret_manager_secret.lab["admin-token"].id
  app_key_secret                 = google_secret_manager_secret.lab["app-key"].id
  admin_allow_cidrs              = [local.control_cidr]
  load_allow_cidrs               = [local.workloads_cidr, local.control_cidr]
  service_account_email          = google_service_account.app.email
  extra_env                      = { APP_VERSION = element(split(":", var.app_image), length(split(":", var.app_image)) - 1) }

  depends_on = [google_secret_manager_secret_version.lab, google_project_iam_member.app]
}

module "load_balancer" {
  count                   = var.enable_workloads ? 1 : 0
  source                  = "../../modules/load-balancer"
  prefix                  = var.prefix
  instance_group          = module.application[0].instance_group
  health_check_id         = module.application[0].ready_health_check_id
  log_sample_rate         = var.lb_log_sample_rate
  labels                  = local.labels
  waiting_room_backend_id = local.waiting_room ? module.waiting_room[0].backend_service_id : null
}

module "waiting_room" {
  count                    = local.waiting_room ? 1 : 0
  source                   = "../../modules/waiting-room"
  prefix                   = var.prefix
  project_id               = var.project_id
  region                   = var.region
  zone                     = var.zone
  subnet_id                = module.networking.workloads_subnet_id
  image                    = var.queue_image
  machine_type             = var.queue_machine_type
  queue_tag                = local.queue_tag
  port                     = var.queue_port
  app_port                 = var.app_port
  labels                   = local.labels
  admin_token_secret       = google_secret_manager_secret.lab["admin-token"].id
  app_backend_service_name = "${var.prefix}-app-backend"
  app_instance_group       = module.application[0].instance_group
  service_account_email    = google_service_account.queue.email
  log_sample_rate          = var.lb_log_sample_rate
  depends_on               = [google_project_iam_member.queue, google_secret_manager_secret_version.lab]
}

module "load_generator" {
  count                 = var.enable_workloads ? 1 : 0
  source                = "../../modules/load-generator"
  prefix                = var.prefix
  project_id            = var.project_id
  zone                  = var.zone
  subnet_id             = module.networking.workloads_subnet_id
  image                 = var.loadgen_image
  machine_type          = var.generator_machine_type
  loadgen_tag           = local.loadgen_tag
  labels                = local.labels
  target_base_url       = module.load_balancer[0].url
  allowed_target_host   = module.load_balancer[0].ip_address
  firestore_database    = google_firestore_database.lab.name
  max_rate              = var.generator_max_rate
  max_vus               = var.generator_max_vus
  max_duration_seconds  = var.max_experiment_seconds
  service_account_email = google_service_account.loadgen.email
  depends_on            = [google_project_iam_member.loadgen]
}

module "control_plane" {
  count                       = var.enable_workloads ? 1 : 0
  source                      = "../../modules/control-plane"
  prefix                      = var.prefix
  region                      = var.region
  image                       = var.control_image
  network_id                  = module.networking.network_id
  subnet_id                   = module.networking.control_subnet_id
  min_instances               = var.control_min_instances
  labels                      = local.labels
  service_account_email       = google_service_account.control.email
  observer_password_secret_id = google_secret_manager_secret.lab["db-observer-password"].secret_id
  admin_token_secret_id       = google_secret_manager_secret.lab["admin-token"].secret_id
  env = {
    GCP_ENABLED                      = "true"
    GCP_PROJECT_ID                   = var.project_id
    GCP_REGION                       = var.region
    GCP_ZONE                         = var.zone
    MIG_NAME                         = module.application[0].mig_name
    AUTOSCALER_NAME                  = module.application[0].autoscaler_name
    BACKEND_SERVICE_NAME             = module.load_balancer[0].backend_service_name
    APP_INSTANCE_PREFIX              = module.application[0].base_instance_name
    FIRESTORE_DATABASE               = google_firestore_database.lab.name
    APPROVED_MAX_INSTANCES           = tostring(var.approved_max_instances)
    BASELINE_MIN_REPLICAS            = tostring(var.min_instances)
    BASELINE_MAX_REPLICAS            = tostring(var.max_instances)
    BASELINE_CPU_TARGET              = tostring(var.target_cpu_utilization)
    BASELINE_INIT_PERIOD_SECONDS     = tostring(var.instance_init_seconds)
    BASELINE_SCALE_IN_WINDOW_SECONDS = tostring(var.scale_in_stabilization)
    TICKETING_PUBLIC_URL             = module.load_balancer[0].url
    TICKETING_ADMIN_PORT             = tostring(var.app_port)
    DB_HOST                          = module.database.host
    DB_PORT                          = "5432"
    DB_NAME                          = module.database.database_name
    DB_USER                          = module.database.observer_user
    DB_SSL                           = "require"
    AUTH_MODE                        = var.auth_mode
    ALLOWED_EMAILS                   = join(",", var.allowed_emails)
    GOOGLE_CLIENT_ID                 = var.google_client_id
    APP_MACHINE_TYPE                 = var.vm_machine_type
    GENERATOR_MACHINE_TYPE           = var.generator_machine_type
    DB_TIER                          = var.db_tier
    LIMIT_MAX_RATE                   = tostring(var.generator_max_rate)
    LIMIT_MAX_VUS                    = tostring(var.generator_max_vus)
    LIMIT_MAX_DURATION_SECONDS       = tostring(var.max_experiment_seconds)
    WAITING_ROOM_URL                 = local.waiting_room ? module.waiting_room[0].private_url : ""
  }
  depends_on = [google_project_iam_member.control, google_secret_manager_secret_version.lab]
}
