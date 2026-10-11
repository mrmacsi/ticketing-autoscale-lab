terraform {
  required_version = ">= 1.9"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 8.6"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.9"
    }
  }

  # Dedicated local state for this lab only (gitignored). Never shared with other projects.
  backend "local" {
    path = "terraform.tfstate"
  }
}

# Credentials: scripts export GOOGLE_OAUTH_ACCESS_TOKEN from
# `gcloud --account=<authorised account> auth print-access-token` immediately before each
# plan/apply/destroy. No key files and no impersonation.
provider "google" {
  project               = var.project_id
  region                = var.region
  zone                  = var.zone
  billing_project       = var.project_id
  user_project_override = true
  default_labels        = local.labels
}
