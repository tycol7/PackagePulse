package agent

import (
	"context"

	"cloud.google.com/go/vertexai/genai"
	"github.com/tylerdean/package-tracker-demo/internal/models"
	"github.com/tylerdean/package-tracker-demo/internal/storage"
)

// LogisticsAgent defines the LLM agent capabilities for package tracking.
type LogisticsAgent interface {
	// ProcessEmail classifies whether the email is a package email and extracts structured data.
	ProcessEmail(ctx context.Context, email storage.ParsedEmail) (*models.ExtractionResult, error)

	// ComposeDigest synthesizes a creative, executive morning briefing for packages arriving today.
	ComposeDigest(ctx context.Context, user *models.User, packages []*models.Package) (string, error)

	// Query processes an interactive reasoning or chat query directed to the agent, invoking tools as needed.
	Query(ctx context.Context, prompt string) (string, error)

	// GetTools returns the function declarations and tool definitions available to the agent.
	GetTools() []*genai.FunctionDeclaration

	// Close cleans up agent resources.
	Close() error
}
