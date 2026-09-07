package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"cloud.google.com/go/vertexai/genai"
	"github.com/tylerdean/package-tracker-demo/internal/models"
	"github.com/tylerdean/package-tracker-demo/internal/storage"
	"github.com/tylerdean/package-tracker-demo/internal/telemetry"
)

// AgentPlatformAgent implements LogisticsAgent using Gemini on Google Cloud Agent Platform.
type AgentPlatformAgent struct {
	client    *genai.Client
	modelName string
}

// NewAgentPlatformAgent initializes an Agent Platform Gemini client.
func NewAgentPlatformAgent(ctx context.Context, projectID, region, modelName string) (*AgentPlatformAgent, error) {
	if region == "" {
		region = "us-central1"
	}
	if modelName == "" {
		modelName = "gemini-2.5-flash"
	}

	client, err := genai.NewClient(ctx, projectID, region)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize Agent Platform client: %w", err)
	}

	return &AgentPlatformAgent{
		client:    client,
		modelName: modelName,
	}, nil
}


func (a *AgentPlatformAgent) Close() error {
	return a.client.Close()
}

func (a *AgentPlatformAgent) ProcessEmail(ctx context.Context, email storage.ParsedEmail) (*models.ExtractionResult, error) {
	model := a.client.GenerativeModel(a.modelName)
	model.ResponseMIMEType = "application/json"
	model.ResponseSchema = &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"is_package_email": {Type: genai.TypeBoolean, Description: "True if email is order/shipping/tracking related; false if promotional/spam/unrelated"},
			"confidence":       {Type: genai.TypeNumber, Description: "Confidence score between 0.0 and 1.0"},
			"reasoning":        {Type: genai.TypeString, Description: "Step-by-step reasoning explaining why this email was or was not classified as tracking/package related"},
			"rejection_reason": {Type: genai.TypeString, Description: "Reason for rejection if not package related"},
			"packages": {
				Type:        genai.TypeArray,
				Description: "List of all packages or shipments found in the email. If multiple tracking numbers or distinct packages are present, extract each one into this list.",
				Items: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"sender":                 {Type: genai.TypeString, Description: "Merchant, store, or shipper name"},
						"carrier":                {Type: genai.TypeString, Description: "Carrier name, e.g. UPS, FedEx, USPS, DHL, Amazon, Other"},
						"tracking_number":        {Type: genai.TypeString, Description: "Tracking number if present (leave empty string if none, never 'null')"},
						"tracking_link":          {Type: genai.TypeString, Description: "Full HTTP/HTTPS tracking URL if present (leave empty string if none, never 'null')"},
						"expected_delivery_date": {Type: genai.TypeString, Description: "Expected delivery date formatted as YYYY-MM-DD if mentioned"},
						"status":                 {Type: genai.TypeString, Description: "Status: Ordered, In Transit, Out for Delivery, Delivered, or Exception"},
						"notes":                  {Type: genai.TypeString, Description: "Brief summary of purchased items or package contents for this package"},
					},
					Required: []string{"sender", "carrier", "status"},
				},
			},
		},
		Required: []string{"is_package_email", "confidence", "reasoning"},
	}

	systemInstruction := `You are an expert logistics and package tracking intake AI agent on Google Cloud Agent Platform.
Analyze the inbound email with transparent step-by-step reasoning (glass box).
1. Determine if this email is directly related to an online purchase, order confirmation, shipment, out for delivery alert, or delivery update.
2. Provide a clear 'reasoning' statement explaining the observed signals (e.g. carrier names, tracking codes, order IDs, or marketing promotions).
	3. If it is NOT related (e.g. marketing newsletter, invoice without shipping, personal email, general spam), mark is_package_email=false with rejection_reason.
4. If it IS related, mark is_package_email=true. Extract ALL packages and tracking numbers present in the email into the 'packages' list. If an email contains multiple packages, split shipments, or multiple tracking numbers, create a separate entry for each in 'packages'.
5. For each package, extract sender, carrier, tracking_number, tracking_link, expected_delivery_date (YYYY-MM-DD), status (Ordered, In Transit, Out for Delivery, Delivered, Exception), and item notes.
6. Deduce Relative Delivery Dates: If the email states the package will be delivered 'tomorrow', 'next day', 'today', or a relative day, compute the exact calendar delivery date in YYYY-MM-DD format using the email's Date Sent as the reference date ONLY if Date Sent is explicitly provided and valid. For example, if Date Sent is 2026-09-04 and the email says 'delivering tomorrow', expected_delivery_date MUST be 2026-09-05. If Date Sent is missing or 'Not specified', DO NOT guess or deduce a date, DO NOT use current time, and leave expected_delivery_date as an empty string "".
CRITICAL: Never output the literal string "null", "None", or "N/A" for tracking_link or tracking_number. If a link or tracking number is not available in the email, return an empty string "".`

	model.SystemInstruction = &genai.Content{
		Parts: []genai.Part{genai.Text(systemInstruction)},
	}

	bodySnippet := email.Body
	if len(bodySnippet) > 12000 {
		bodySnippet = bodySnippet[:12000]
	}

	prompt := fmt.Sprintf("Date Sent: %s\nSubject: %s\nFrom: %s\n\nEmail Body Content:\n%s", email.DateString(), email.Subject, email.From, bodySnippet)

	ctx, llmSpan := telemetry.StartLLMSpan(ctx, "model.generate_content",
		trace.WithAttributes(
			attribute.String("gen_ai.system", "agent_platform"),
			attribute.String("gen_ai.request.model", a.modelName),
			attribute.String("gcp.agent.llm_request", prompt),
		),
	)
	resp, err := model.GenerateContent(ctx, genai.Text(prompt))
	if err != nil {
		llmSpan.RecordError(err)
		llmSpan.End()
		return nil, fmt.Errorf("gemini extraction call failed: %w", err)
	}

	if resp != nil && resp.UsageMetadata != nil {
		llmSpan.SetAttributes(
			attribute.Int("gen_ai.usage.input_tokens", int(resp.UsageMetadata.PromptTokenCount)),
			attribute.Int("gen_ai.usage.output_tokens", int(resp.UsageMetadata.CandidatesTokenCount)),
		)
	}

	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		llmSpan.End()
		return nil, fmt.Errorf("empty response from gemini")
	}

	rawJSON := fmt.Sprintf("%v", resp.Candidates[0].Content.Parts[0])
	llmSpan.SetAttributes(attribute.String("gcp.agent.llm_response", rawJSON))
	llmSpan.End()

	var result models.ExtractionResult
	if err := json.Unmarshal([]byte(rawJSON), &result); err != nil {
		return nil, fmt.Errorf("failed to parse gemini json output: %w (raw: %s)", err, rawJSON)
	}

	// Apply deterministic date deduction for tomorrow/relative delivery if indicated
	combinedText := email.Subject + " " + email.Body
	hasRelativeDelivery := storage.HasTomorrowDelivery(combinedText) || storage.HasTodayDelivery(combinedText)
	for _, p := range result.AllPackages() {
		if email.SentAt != nil && hasRelativeDelivery {
			p.ExpectedDeliveryDate = storage.DeduceDeliveryDate(combinedText, email.SentAt)
		} else if email.SentAt == nil && hasRelativeDelivery {
			// Without a sent date header, we must never guess or use current time
			p.ExpectedDeliveryDate = ""
		}
	}

	_ = result.AllPackages()
	return &result, nil
}

func (a *AgentPlatformAgent) ComposeDigest(ctx context.Context, user *models.User, packages []*models.Package) (string, error) {
	model := a.client.GenerativeModel(a.modelName)

	todayStr := time.Now().Format("Monday, January 2, 2006")
	pkgBytes, _ := json.MarshalIndent(packages, "", "  ")

	prompt := fmt.Sprintf(`You are an executive personal logistics assistant for Google engineer %s.
Compose a stylish, encouraging, and clear HTML email briefing for packages arriving today (%s).

User Timezone: %s
Package manifest for today:
%s

Formatting guidelines:
1. Warm, executive greeting to %s.
2. High-level summary count of packages arriving today.
3. A styled card or list for each package including:
   - Merchant/Sender
   - Carrier & Tracking number (include clickable tracking link if present)
   - Current status
   - Notes / Item description
4. A short, helpful tip (e.g. weather protection, signing for delivery, or porch awareness).
5. Clean inline CSS styling adhering to Google Material Design (clean cards, subtle rounded borders, Roboto font, Google colors #1a73e8, #34a853, #f8f9fa).
6. Output ONLY the valid HTML markup, without markdown code fences.`,
		user.DisplayName, todayStr, user.Preferences.Timezone, string(pkgBytes), user.DisplayName)

	ctx, llmSpan := telemetry.StartLLMSpan(ctx, "model.generate_content")
	resp, err := model.GenerateContent(ctx, genai.Text(prompt))
	if err != nil {
		llmSpan.RecordError(err)
		llmSpan.End()
		return "", fmt.Errorf("gemini digest composition failed: %w", err)
	}

	if resp != nil && resp.UsageMetadata != nil {
		llmSpan.SetAttributes(
			attribute.Int("gen_ai.usage.input_tokens", int(resp.UsageMetadata.PromptTokenCount)),
			attribute.Int("gen_ai.usage.output_tokens", int(resp.UsageMetadata.CandidatesTokenCount)),
		)
	}
	llmSpan.End()

	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("empty digest response from gemini")
	}

	content := fmt.Sprintf("%v", resp.Candidates[0].Content.Parts[0])
	content = strings.TrimPrefix(content, "```html")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	return strings.TrimSpace(content), nil
}

func (a *AgentPlatformAgent) Query(ctx context.Context, prompt string) (string, error) {
	model := a.client.GenerativeModel(a.modelName)
	model.SystemInstruction = &genai.Content{
		Parts: []genai.Part{genai.Text("You are the PackagePulse Logistics Agent deployed on Google Cloud Agent Platform. You assist users with tracking packages, analyzing shipping updates, classifying inbound delivery emails, and summarizing logistics briefings. Answer concisely, professionally, and helpfully.")},
	}
	ctx, llmSpan := telemetry.StartLLMSpan(ctx, "model.generate_content")
	resp, err := model.GenerateContent(ctx, genai.Text(prompt))
	if err != nil {
		llmSpan.RecordError(err)
		llmSpan.End()
		return "", fmt.Errorf("gemini query failed: %w", err)
	}

	if resp != nil && resp.UsageMetadata != nil {
		llmSpan.SetAttributes(
			attribute.Int("gen_ai.usage.input_tokens", int(resp.UsageMetadata.PromptTokenCount)),
			attribute.Int("gen_ai.usage.output_tokens", int(resp.UsageMetadata.CandidatesTokenCount)),
		)
	}
	llmSpan.End()

	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return "No response generated.", nil
	}
	return fmt.Sprintf("%v", resp.Candidates[0].Content.Parts[0]), nil
}

