#!/usr/bin/env bash
set -euo pipefail

# PackagePulse Cloud Run + Terraform Deployment Script
# Usage: ./scripts/deploy.sh <PROJECT_ID> [REGION]

PROJECT_ID="${1:-${PROJECT_ID:-$(gcloud config get-value project 2>/dev/null || true)}}"
REGION="${2:-${REGION:-us-central1}}"

if [[ -z "$PROJECT_ID" || "$PROJECT_ID" == "(unset)" ]]; then
  echo "❌ Error: Google Cloud PROJECT_ID must be specified."
  echo "Usage: ./scripts/deploy.sh <PROJECT_ID> [REGION]"
  echo "Example: ./scripts/deploy.sh my-argolis-project-12345"
  exit 1
fi

echo "=================================================================="
echo "🚀 DEPLOYING PACKAGEPULSE (Google Cloud AI Agent)"
echo "📍 Target GCP Project: $PROJECT_ID"
echo "📍 Region:            $REGION"
echo "=================================================================="

# Set active gcloud project
gcloud config set project "$PROJECT_ID"

# 1. Clean local artifacts so they are not uploaded in the tarball
rm -f server

# 2. Enable Cloud Build, IAM, Agent Platform, Telemetry, and Monitoring APIs
echo "🔧 Ensuring Cloud Build, IAM, Agent Platform, Cloud Trace, Logging, and Monitoring APIs are enabled..."
gcloud services enable \
  aiplatform.googleapis.com \
  agentidentity.googleapis.com \
  agentregistry.googleapis.com \
  artifactregistry.googleapis.com \
  cloudbuild.googleapis.com \
  cloudtrace.googleapis.com \
  logging.googleapis.com \
  monitoring.googleapis.com \
  telemetry.googleapis.com \
  observability.googleapis.com \
  iam.googleapis.com \
  iap.googleapis.com \
  discoveryengine.googleapis.com \
  apphub.googleapis.com \
  apptopology.googleapis.com \
  --project="$PROJECT_ID"

gcloud services enable \
  cloudapiregistry.googleapis.com \
  dataform.googleapis.com \
  modelarmor.googleapis.com \
  networksecurity.googleapis.com \
  networkservices.googleapis.com \
  notebooks.googleapis.com \
  saasservicemgmt.googleapis.com \
  securitycenter.googleapis.com \
  texttospeech.googleapis.com \
  --project="$PROJECT_ID"

# 3. Retrieve Project Number
PROJECT_NUMBER=$(gcloud projects describe "$PROJECT_ID" --format="value(projectNumber)")
echo "ℹ️  GCP Project Number: $PROJECT_NUMBER"

# 4. Configure Cloud Build Execution Identities, Artifact Registry & Agent Runtime Permissions
echo "🔑 Provisioning Cloud Build, Artifact Registry, and Agent Runtime Service Identities and Permissions..."

# Create Cloud Build Service Agent if not yet initialized
gcloud beta services identity create --service=cloudbuild.googleapis.com --project="$PROJECT_ID" 2>/dev/null || true

# Ensure Artifact Registry repository exists for Agent Runtime
gcloud artifacts repositories create agents \
  --repository-format=docker \
  --location="$REGION" \
  --description="Agent Platform and Agent Runtime container images" \
  --project="$PROJECT_ID" 2>/dev/null || true

# Grant Storage Admin & Builder roles to the Cloud Build execution identities
# (Resolves: could not resolve source / generic::permission_denied)
BUILD_IDENTITIES=(
  "${PROJECT_NUMBER}@cloudbuild.gserviceaccount.com"
  "${PROJECT_NUMBER}-compute@developer.gserviceaccount.com"
  "service-${PROJECT_NUMBER}@gcp-sa-cloudbuild.iam.gserviceaccount.com"
)

for SA in "${BUILD_IDENTITIES[@]}"; do
  echo "   - Granting build permissions to $SA..."
  gcloud projects add-iam-policy-binding "$PROJECT_ID" \
    --member="serviceAccount:${SA}" \
    --role="roles/storage.admin" \
    --condition=None --quiet 2>/dev/null || true

  gcloud projects add-iam-policy-binding "$PROJECT_ID" \
    --member="serviceAccount:${SA}" \
    --role="roles/cloudbuild.builds.builder" \
    --condition=None --quiet 2>/dev/null || true

  gcloud projects add-iam-policy-binding "$PROJECT_ID" \
    --member="serviceAccount:${SA}" \
    --role="roles/logging.logWriter" \
    --condition=None --quiet 2>/dev/null || true
done

# Grant Artifact Registry Reader to Agent Platform and Reasoning Engine Service Agents
gcloud projects add-iam-policy-binding "$PROJECT_ID" \
  --member="serviceAccount:service-${PROJECT_NUMBER}@gcp-sa-aiplatform.iam.gserviceaccount.com" \
  --role="roles/artifactregistry.reader" \
  --condition=None --quiet 2>/dev/null || true

gcloud projects add-iam-policy-binding "$PROJECT_ID" \
  --member="serviceAccount:service-${PROJECT_NUMBER}@gcp-sa-aiplatform-re.iam.gserviceaccount.com" \
  --role="roles/artifactregistry.reader" \
  --condition=None --quiet 2>/dev/null || true

# Ensure the staging bucket permissions are open to build identities if the bucket already exists
STAGING_BUCKET="gs://${PROJECT_ID}_cloudbuild"
if gcloud storage buckets describe "$STAGING_BUCKET" --project="$PROJECT_ID" &>/dev/null; then
  for SA in "${BUILD_IDENTITIES[@]}"; do
    gcloud storage buckets add-iam-policy-binding "$STAGING_BUCKET" \
      --member="serviceAccount:${SA}" \
      --role="roles/storage.admin" 2>/dev/null || true
  done
fi

# Allow 3 seconds for IAM propagation
sleep 3

# 5. Build Container via Cloud Build
echo "📦 Building container image via Google Cloud Build..."
gcloud builds submit --config=cloudbuild.yaml .

# 6. Provision Infrastructure via Terraform
echo "🏗️ Provisioning infrastructure with Terraform..."
if ! command -v terraform &>/dev/null; then
  echo "📥 Installing Terraform into ~/.local/bin..."
  mkdir -p ~/.local/bin
  curl -fsSL https://releases.hashicorp.com/terraform/1.10.5/terraform_1.10.5_linux_amd64.zip -o /tmp/terraform.zip
  unzip -q -o /tmp/terraform.zip -d ~/.local/bin/
  rm /tmp/terraform.zip
  export PATH="$HOME/.local/bin:$PATH"
fi

cd terraform

export GOOGLE_OAUTH_ACCESS_TOKEN="$(gcloud auth print-access-token)"

terraform init -upgrade
terraform apply -auto-approve \
  -var="project_id=${PROJECT_ID}" \
  -var="region=${REGION}"

# Ensure Model Armor template exists with SDP basic config for sensitive data protection
echo "🛡️  Ensuring Model Armor template 'email-armor-guard' is provisioned with SDP..."
gcloud model-armor templates create email-armor-guard \
  --project="$PROJECT_ID" \
  --location="$REGION" \
  --pi-and-jailbreak-filter-settings-enforcement=enabled \
  --pi-and-jailbreak-filter-settings-confidence-level=medium-and-above \
  --malicious-uri-filter-settings-enforcement=enabled \
  --basic-config-filter-enforcement=enabled --quiet 2>/dev/null || \
gcloud model-armor templates update email-armor-guard \
  --project="$PROJECT_ID" \
  --location="$REGION" \
  --pi-and-jailbreak-filter-settings-enforcement=enabled \
  --pi-and-jailbreak-filter-settings-confidence-level=medium-and-above \
  --malicious-uri-filter-settings-enforcement=enabled \
  --basic-config-filter-enforcement=enabled --quiet 2>/dev/null || true

# Ensure public web access without IAM check (compatible with org-restricted projects)
echo "🌐 Ensuring public web ingress without IAM check..."
gcloud run services update package-tracker \
  --project="$PROJECT_ID" \
  --region="$REGION" \
  --update-env-vars="OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=true,OTEL_SEMCONV_STABILITY_OPT_IN=gen_ai_latest_experimental,MODEL_ARMOR_TEMPLATE=email-armor-guard,PROJECT_NUMBER=${PROJECT_NUMBER}" \
  --no-invoker-iam-check --quiet

# 7. Deploy / Update Reasoning Engine on Agent Runtime
echo "🤖 Registering / Updating PackagePulse Logistics Agent on Agent Runtime..."
RE_IMAGE="${REGION}-docker.pkg.dev/${PROJECT_ID}/agents/package-tracker:latest"
RE_DISPLAY_NAME="PackagePulse Logistics Agent"

EXISTING_RE_NAME=$(curl -s -H "Authorization: Bearer $(gcloud auth print-access-token)" \
  "https://${REGION}-aiplatform.googleapis.com/v1beta1/projects/${PROJECT_ID}/locations/${REGION}/reasoningEngines" | \
  grep -B 2 "\"displayName\": \"${RE_DISPLAY_NAME}\"" | grep "\"name\":" | head -n 1 | awk -F'"' '{print $4}' || true)

RE_PAYLOAD=$(cat <<EOF
{
  "displayName": "${RE_DISPLAY_NAME}",
  "description": "Autonomous Logistics & Tracking Inbound Email Classifier Agent",
  "spec": {
    "deploymentSpec": {
      "env": [
        {"name": "GOOGLE_CLOUD_AGENT_ENGINE_ENABLE_TELEMETRY", "value": "true"},
        {"name": "OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT", "value": "true"},
        {"name": "OTEL_SEMCONV_STABILITY_OPT_IN", "value": "gen_ai_latest_experimental"},
        {"name": "PROJECT_ID", "value": "${PROJECT_ID}"},
        {"name": "REGION", "value": "${REGION}"}
      ]
    },
    "containerSpec": {
      "imageUri": "${RE_IMAGE}"
    }
  }
}
EOF
)

if [[ -n "$EXISTING_RE_NAME" ]]; then
  echo "   - Updating existing Reasoning Engine deployment ($EXISTING_RE_NAME)..."
  curl -s -X PATCH \
    -H "Authorization: Bearer $(gcloud auth print-access-token)" \
    -H "Content-Type: application/json" \
    -d "$RE_PAYLOAD" \
    "https://${REGION}-aiplatform.googleapis.com/v1beta1/${EXISTING_RE_NAME}?updateMask=spec.containerSpec.imageUri,spec.deploymentSpec.env,displayName,description" > /dev/null
  echo "   ✅ Agent Runtime deployment updated successfully."
else
  echo "   - Creating new Reasoning Engine deployment on Agent Runtime..."
  curl -s -X POST \
    -H "Authorization: Bearer $(gcloud auth print-access-token)" \
    -H "Content-Type: application/json" \
    -d "$RE_PAYLOAD" \
    "https://${REGION}-aiplatform.googleapis.com/v1beta1/projects/${PROJECT_ID}/locations/${REGION}/reasoningEngines" > /dev/null
  echo "   ✅ Agent Runtime deployment initiated successfully."
fi

APP_URL=$(terraform output -raw app_url)
OAUTH_SECRET_ID=$(terraform output -raw oauth_secret_id)

echo ""
echo "=================================================================="
echo "🎉 INFRASTRUCTURE & APPLICATION DEPLOYED SUCCESSFULLY!"
echo "🌐 Application URL: $APP_URL"
echo ""
echo "🔑 GOOGLE OAUTH 2.0 ONE-TIME SETUP:"
echo "1. Visit the Google Cloud Console Credentials page:"
echo "   https://console.cloud.google.com/apis/credentials/oauthclient?project=${PROJECT_ID}"
echo ""
echo "2. Configure the OAuth Client:"
echo "   - Application type: 'Web application'"
echo "   - Name: 'PackagePulse'"
echo "   - Authorized redirect URIs: ${APP_URL}/auth/callback"
echo ""
echo "3. Save your Client ID & Secret by running:"
echo "   ./scripts/setup-oauth.sh <CLIENT_ID> <CLIENT_SECRET> ${PROJECT_ID} ${REGION}"
echo "=================================================================="
