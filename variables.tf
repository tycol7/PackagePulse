variable "project_id" {
  description = "The Google Cloud Project ID to deploy Package Tracker into"
  type        = string
}

variable "region" {
  description = "The GCP region for Cloud Run, Firestore, and GCS"
  type        = string
  default     = "us-central1"
}

variable "app_name" {
  description = "Prefix name for all resources"
  type        = string
  default     = "package-tracker"
}

variable "google_client_id" {
  description = "Google OAuth 2.0 Web Client ID"
  type        = string
  default     = ""
}
