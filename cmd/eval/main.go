package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/tylerdean/package-tracker-demo/internal/agent"
	"github.com/tylerdean/package-tracker-demo/internal/config"
	"github.com/tylerdean/package-tracker-demo/internal/eval"
	"github.com/tylerdean/package-tracker-demo/internal/security"
)

func findDatasetFile(providedPath string) (string, error) {
	candidates := []string{
		providedPath,
		"eval/golden_dataset.json",
		"tests/eval/golden_dataset.json",
		"../eval/golden_dataset.json",
		"../../eval/golden_dataset.json",
	}

	for _, p := range candidates {
		if p == "" {
			continue
		}
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, nil
		}
	}

	return "", fmt.Errorf("could not locate golden dataset file (checked: %v)", candidates)
}

func main() {
	datasetFlag := flag.String("dataset", "eval/golden_dataset.json", "Path to golden evaluation benchmark dataset JSON")
	useLiveFlag := flag.Bool("live", false, "Execute evaluation against live Google Cloud Agent Platform & Model Armor")
	jsonOutputFlag := flag.String("output-json", "", "Optional path to write full evaluation report JSON")
	minScoreFlag := flag.Float64("min-score", 100.0, "Minimum overall benchmark score required to pass (0-100)")
	flag.Parse()

	ctx := context.Background()

	resolvedDatasetPath, err := findDatasetFile(*datasetFlag)
	if err != nil {
		log.Fatalf("❌ Golden dataset file error: %v", err)
	}

	ds, err := eval.LoadGoldenDataset(resolvedDatasetPath)
	if err != nil {
		log.Fatalf("❌ Failed to load golden dataset from %s: %v", resolvedDatasetPath, err)
	}

	var ag agent.LogisticsAgent
	var armor security.ModelArmorService

	if *useLiveFlag {
		cfg := config.Load(ctx)
		if cfg.ProjectID == "" {
			log.Fatalf("❌ Cannot run live evaluation: GCP_PROJECT_ID / PROJECT_ID environment variable not set")
		}
		fmt.Printf("🌐 Initializing live Google Cloud Agent Platform (Project: %s, Region: %s)...\n", cfg.ProjectID, cfg.Region)
		liveAgent, err := agent.NewAgentPlatformAgent(ctx, cfg.ProjectID, cfg.Region, cfg.GeminiModel)
		if err != nil {
			log.Fatalf("❌ Failed to initialize live Agent Platform agent: %v", err)
		}
		defer liveAgent.Close()
		ag = liveAgent

		liveArmor, err := security.NewCloudModelArmorService(ctx, cfg.ProjectID, cfg.Region, cfg.ModelArmorTemplate)
		if err != nil {
			log.Fatalf("❌ Failed to initialize live Model Armor service: %v", err)
		}
		defer liveArmor.Close()
		armor = liveArmor
	} else {
		ag = agent.NewMockAgent()
		armor = security.NewMockModelArmorService()
	}

	fmt.Printf("🔍 Running PackagePulse Agent Evaluation Suite against: %s\n", resolvedDatasetPath)
	fmt.Printf("📋 Total benchmark test cases: %d\n", len(ds.Cases))

	report := eval.RunEvaluation(ctx, ds, ag, armor)

	// Print formatted scorecard to stdout
	fmt.Println(report.FormatScorecard())

	// Optionally export machine-readable JSON report
	if *jsonOutputFlag != "" {
		reportData, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			log.Printf("⚠️  Failed marshaling report to JSON: %v", err)
		} else {
			if err := os.WriteFile(*jsonOutputFlag, reportData, 0644); err != nil {
				log.Printf("⚠️  Failed saving JSON report to %s: %v", *jsonOutputFlag, err)
			} else {
				fmt.Printf("💾 Machine-readable evaluation report saved to: %s\n", *jsonOutputFlag)
			}
		}
	}

	if report.PassedCases < report.TotalCases || report.OverallScore < *minScoreFlag {
		fmt.Printf("❌ Evaluation failed: %d failed test cases, score %.1f < required %.1f\n",
			report.FailedCases, report.OverallScore, *minScoreFlag)
		os.Exit(1)
	}

	fmt.Printf("✅ All %d regression benchmark cases passed with score %.1f/100.0!\n", report.TotalCases, report.OverallScore)
}
