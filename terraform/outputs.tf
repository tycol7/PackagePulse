output "app_url" {
  description = "The public URL of the deployed Cloud Run service"
  value       = google_cloud_run_v2_service.app.uri
}

output "gcs_bucket" {
  description = "The Google Cloud Storage bucket created for raw email payloads"
  value       = google_storage_bucket.raw_emails.name
}

output "service_account" {
  description = "The dedicated least-privilege service account used by Cloud Run"
  value       = google_service_account.app_sa.email
}

output "oauth_secret_id" {
  description = "The Secret Manager secret ID for storing the Google OAuth client secret"
  value       = google_secret_manager_secret.oauth_secret.secret_id
}
