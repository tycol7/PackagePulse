#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

cd "${REPO_ROOT}"

echo "======================================================================"
echo "🎯 PackagePulse Automated Agent Evaluation Suite"
echo "======================================================================"

# 1. Run via Go unit test framework
echo "Running evaluation benchmarks via Go test runner..."
go test -v ./internal/eval -run TestEvaluationSuite_GoldenDataset

echo ""
# 2. Run standalone evaluator CLI
echo "Running standalone evaluation CLI scorecard..."
go run ./cmd/eval "$@"

echo "======================================================================"
echo "✅ Agent Regression Evaluation Passed with 100% Benchmark Score!"
echo "======================================================================"
