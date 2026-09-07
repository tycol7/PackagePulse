#!/usr/bin/env bash
set -euo pipefail

# Clean Teardown Script for PackagePulse
# Usage: ./scripts/teardown.sh <PROJECT_ID> [REGION]

PROJECT_ID="${1:-${PROJECT_ID:-$(gcloud config get-value project 2>/dev/null || true)}}"
REGION="${2:-${REGION:-us-central1}}"

if [[ -z "$PROJECT_ID" || "$PROJECT_ID" == "(unset)" ]]; then
  echo "Usage: ./scripts/teardown.sh <PROJECT_ID> [REGION]"
  exit 1
fi

echo "⚠️  Destroying all Terraform-managed resources in project: $PROJECT_ID"
read -p "Are you sure you want to proceed? (y/N): " -n 1 -r
echo
if [[ $REPLY =~ ^[Yy]$ ]]; then
  cd terraform
  terraform destroy -auto-approve \
    -var="project_id=${PROJECT_ID}" \
    -var="region=${REGION}"
  echo "✅ Teardown complete."
else
  echo "Aborted."
fi
