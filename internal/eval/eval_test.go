package eval_test

import (
	"context"
	"testing"

	"github.com/tylerdean/package-tracker-demo/internal/agent"
	"github.com/tylerdean/package-tracker-demo/internal/eval"
	"github.com/tylerdean/package-tracker-demo/internal/security"
)

func TestEvaluationSuite_GoldenDataset(t *testing.T) {
	ctx := context.Background()
	datasetPath := "../../eval/golden_dataset.json"

	ds, err := eval.LoadGoldenDataset(datasetPath)
	if err != nil {
		t.Fatalf("failed loading golden dataset: %v", err)
	}

	mockAgent := agent.NewMockAgent()
	mockArmor := security.NewMockModelArmorService()

	report := eval.RunEvaluation(ctx, ds, mockAgent, mockArmor)

	t.Log(report.FormatScorecard())

	if report.PassedCases != report.TotalCases {
		t.Errorf("evaluation regressions detected: %d / %d cases failed", report.FailedCases, report.TotalCases)
	}

	if report.ClassificationAccuracy < 100.0 {
		t.Errorf("classification accuracy regression: %.1f%%", report.ClassificationAccuracy)
	}

	if report.ExtractionAccuracy < 100.0 {
		t.Errorf("extraction accuracy regression: %.1f%%", report.ExtractionAccuracy)
	}

	if report.SecurityBlockRate < 100.0 {
		t.Errorf("adversarial defense rate regression: %.1f%%", report.SecurityBlockRate)
	}

	if report.PIIRedactionRate < 100.0 {
		t.Errorf("PII redaction rate regression: %.1f%%", report.PIIRedactionRate)
	}
}
