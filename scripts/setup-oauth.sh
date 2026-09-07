#!/usr/bin/env bash
set -euo pipefail

# Script to configure Google OAuth 2.0 Credentials into Secret Manager & Cloud Run
# Usage: ./scripts/setup-oauth.sh <CLIENT_ID> <CLIENT_SECRET> [PROJECT_ID] [REGION]

CLIENT_ID="${1:-}"
CLIENT_SECRET="${2:-}"
PROJECT_ID="${3:-${PROJECT_ID:-$(gcloud config get-value project 2>/dev/null || true)}}"
REGION="${4:-${REGION:-us-central1}}"

if [[ -z "$CLIENT_ID" || -z "$CLIENT_SECRET" ]]; then
  echo "Usage: ./scripts/setup-oauth.sh <CLIENT_ID> <CLIENT_SECRET> [PROJECT_ID] [REGION]"
  exit 1
fi

echo "🔐 Saving OAuth Client Secret to Google Secret Manager..."
echo -n "$CLIENT_SECRET" | gcloud secrets versions add package-tracker-oauth-secret \
  --project="$PROJECT_ID" \
  --data-file=-

APP_URL=$(gcloud run services describe package-tracker --project="$PROJECT_ID" --region="$REGION" --format="value(status.url)")

echo "🚀 Updating Cloud Run with GOOGLE_CLIENT_ID and OAUTH_REDIRECT_URL..."
gcloud run services update package-tracker \
  --project="$PROJECT_ID" \
  --region="$REGION" \
  --no-invoker-iam-check \
  --update-env-vars="GOOGLE_CLIENT_ID=${CLIENT_ID},OAUTH_REDIRECT_URL=${APP_URL}/auth/callback"

# Persist client ID to local terraform.tfvars so future terraform applies do not overwrite it
if [[ -d "terraform" ]]; then
  cat <<EOF > terraform/terraform.tfvars
project_id       = "${PROJECT_ID}"
region           = "${REGION}"
app_name         = "package-tracker"
google_client_id = "${CLIENT_ID}"
EOF
fi

echo ""
echo "=================================================================="
echo "✅ Google OAuth 2.0 configured successfully!"
echo "🌐 Open: $APP_URL"
echo "👉 Click 'Sign in with Google' to authenticate with your @google.com account."
echo "=================================================================="
