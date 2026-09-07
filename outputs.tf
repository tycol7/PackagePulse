output "app_url" {
  description = "The public URL of the deployed Cloud Run service"
  value       = module.package_tracker.app_url
}

output "gcs_bucket" {
  description = "The Google Cloud Storage bucket created for PII-redacted email payloads"
  value       = module.package_tracker.gcs_bucket
}

output "service_account" {
  description = "The dedicated least-privilege service account used by Cloud Run"
  value       = module.package_tracker.service_account
}
