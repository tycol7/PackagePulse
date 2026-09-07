package agent

import (
	"context"

	"github.com/tylerdean/package-tracker-demo/internal/models"
	"github.com/tylerdean/package-tracker-demo/internal/storage"
)

// LogisticsAgent defines the LLM agent capabilities for package tracking.
type LogisticsAgent interface {
	// ProcessEmail classifies whether the email is a package email and extracts structured data.
	ProcessEmail(ctx context.Context, email storage.ParsedEmail) (*models.ExtractionResult, error)

	// ComposeDigest synthesizes a creative, executive morning briefing for packages arriving today.
	ComposeDigest(ctx context.Context, user *models.User, packages []*models.Package) (string, error)

	// Query processes an interactive reasoning or chat query directed to the agent.
	Query(ctx context.Context, prompt string) (string, error)

	// Close cleans up agent resources.
	Close() error
}
