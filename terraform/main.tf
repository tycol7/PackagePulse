terraform {
  required_version = ">= 1.5.0"
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 6.0"
    }
  }
}

provider "google" {
  project = var.project_id
  region  = var.region
}

# 1. Enable Required Google Cloud APIs (including Agent Platform, Runtime, and Telemetry)
resource "google_project_service" "apis" {
  for_each = toset([
    "run.googleapis.com",
    "firestore.googleapis.com",
    "aiplatform.googleapis.com",
    "storage.googleapis.com",
    "cloudbuild.googleapis.com",
    "secretmanager.googleapis.com",
    "iam.googleapis.com",
    "cloudtrace.googleapis.com",
    "logging.googleapis.com",
    "monitoring.googleapis.com",
    "discoveryengine.googleapis.com",
    "telemetry.googleapis.com",
    "clouderrorreporting.googleapis.com",
    "observability.googleapis.com",
    "artifactregistry.googleapis.com",
    "agentidentity.googleapis.com",
    "agentregistry.googleapis.com",
    "apphub.googleapis.com",
    "apptopology.googleapis.com",
    "cloudapiregistry.googleapis.com",
    "dataform.googleapis.com",
    "iap.googleapis.com",
    "modelarmor.googleapis.com",
    "networksecurity.googleapis.com",
    "networkservices.googleapis.com",
    "notebooks.googleapis.com",
    "saasservicemgmt.googleapis.com",
    "securitycenter.googleapis.com",
    "texttospeech.googleapis.com",
  ])
  service            = each.key
  disable_on_destroy = false
}

# 2. Cloud Firestore (Native Mode) Database
resource "google_firestore_database" "database" {
  depends_on  = [google_project_service.apis]
  name        = "(default)"
  location_id = var.region
  type        = "FIRESTORE_NATIVE"
}

# 3. Google Cloud Storage Bucket for Inbound Email Raw Payloads
resource "google_storage_bucket" "raw_emails" {
  depends_on                  = [google_project_service.apis]
  name                        = "${var.project_id}-${var.app_name}-raw-emails"
  location                    = var.region
  uniform_bucket_level_access = true
  force_destroy               = true

  lifecycle_rule {
    action {
      type = "Delete"
    }
    condition {
      age = 90
    }
  }
}

# 4. Secret Manager for Google OAuth Client Secret
resource "google_secret_manager_secret" "oauth_secret" {
  depends_on = [google_project_service.apis]
  secret_id  = "${var.app_name}-oauth-secret"

  replication {
    auto {}
  }
}

# 5. Dedicated Service Account (Strict Least-Privilege IAM)
resource "google_service_account" "app_sa" {
  depends_on   = [google_project_service.apis]
  account_id   = "${var.app_name}-sa"
  display_name = "Service Account for Package Tracker Cloud Run"
}

# Grant Firestore Access (datastore.user)
resource "google_project_iam_member" "firestore_user" {
  project = var.project_id
  role    = "roles/datastore.user"
  member  = "serviceAccount:${google_service_account.app_sa.email}"
}

# Grant Agent Platform Access (aiplatform.user)
resource "google_project_iam_member" "agent_platform_user" {
  project = var.project_id
  role    = "roles/aiplatform.user"
  member  = "serviceAccount:${google_service_account.app_sa.email}"
}

# Grant GCS Bucket Access strictly scoped to the raw emails bucket
resource "google_storage_bucket_iam_member" "gcs_object_user" {
  bucket = google_storage_bucket.raw_emails.name
  role   = "roles/storage.objectUser"
  member = "serviceAccount:${google_service_account.app_sa.email}"
}

# Grant Secret Manager Access to read the OAuth secret
resource "google_secret_manager_secret_iam_member" "secret_accessor" {
  secret_id = google_secret_manager_secret.oauth_secret.id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.app_sa.email}"
}

# Grant Cloud Trace Agent Access
resource "google_project_iam_member" "trace_agent" {
  project = var.project_id
  role    = "roles/cloudtrace.agent"
  member  = "serviceAccount:${google_service_account.app_sa.email}"
}

# Grant Cloud Logging LogWriter Access
resource "google_project_iam_member" "log_writer" {
  project = var.project_id
  role    = "roles/logging.logWriter"
  member  = "serviceAccount:${google_service_account.app_sa.email}"
}

# Grant Model Armor Access to sanitize prompts
resource "google_project_iam_member" "modelarmor_user" {
  project = var.project_id
  role    = "roles/modelarmor.user"
  member  = "serviceAccount:${google_service_account.app_sa.email}"
}

# 6. Cloud Run Service (Serverless, scale-to-zero)
resource "google_cloud_run_v2_service" "app" {
  depends_on = [
    google_project_service.apis,
    google_firestore_database.database,
    google_storage_bucket.raw_emails,
    google_secret_manager_secret.oauth_secret
  ]
  name                 = var.app_name
  location             = var.region
  ingress              = "INGRESS_TRAFFIC_ALL"
  invoker_iam_disabled = true

  template {
    service_account = google_service_account.app_sa.email

    scaling {
      min_instance_count = 0 # Scales to 0 when idle ($0 idle compute cost)
      max_instance_count = 2
    }

    containers {
      image = "gcr.io/${var.project_id}/${var.app_name}:latest"

      resources {
        limits = {
          cpu    = "1000m"
          memory = "512Mi"
        }
      }

      env {
        name  = "PROJECT_ID"
        value = var.project_id
      }
      env {
        name  = "GCS_BUCKET_NAME"
        value = google_storage_bucket.raw_emails.name
      }
      env {
        name  = "FIRESTORE_DATABASE_ID"
        value = google_firestore_database.database.name
      }
      env {
        name  = "GOOGLE_CLIENT_ID"
        value = var.google_client_id
      }
      env {
        name  = "SECRET_NAME_OAUTH"
        value = google_secret_manager_secret.oauth_secret.secret_id
      }
      env {
        name  = "REGION"
        value = var.region
      }
      env {
        name  = "OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT"
        value = "true"
      }
      env {
        name  = "OTEL_SEMCONV_STABILITY_OPT_IN"
        value = "gen_ai_latest_experimental"
      }
    }
  }
}

