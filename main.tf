# PackagePulse Root Terraform Entrypoint
# Delegates to the terraform/ module infrastructure definition.

terraform {
  required_version = ">= 1.5.0"
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = ">= 5.0.0"
    }
  }
}

module "package_tracker" {
  source           = "./terraform"
  project_id       = var.project_id
  region           = var.region
  app_name         = var.app_name
  google_client_id = var.google_client_id
}
