package security

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	modelarmor "cloud.google.com/go/modelarmor/apiv1"
	"cloud.google.com/go/modelarmor/apiv1/modelarmorpb"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/api/option"

	"github.com/tylerdean/package-tracker-demo/internal/telemetry"
)

// SanitizeResult describes the verdict of screening a prompt via Google Cloud Model Armor.
type SanitizeResult struct {
	Passed            bool     `json:"passed"`
	MatchFound        bool     `json:"match_found"`
	JailbreakDetected bool     `json:"jailbreak_detected"`
	Confidence        string   `json:"confidence,omitempty"`
	BlockedReason     string   `json:"blocked_reason,omitempty"`
	SanitizedText     string   `json:"sanitized_text,omitempty"`
	PIIFound          bool     `json:"pii_found,omitempty"`
	InfoTypes         []string `json:"info_types,omitempty"`
	Details           string   `json:"details,omitempty"`
}

// ModelArmorService abstracts prompt screening and sanitization.
type ModelArmorService interface {
	ScreenPrompt(ctx context.Context, text string) (*SanitizeResult, error)
	Close() error
}

// CloudModelArmorService uses the Google Cloud Model Armor v1 API.
type CloudModelArmorService struct {
	client       *modelarmor.Client
	templateName string
}

// NewCloudModelArmorService creates a client configured with a Model Armor template.
func NewCloudModelArmorService(ctx context.Context, projectID, region, templateID string) (*CloudModelArmorService, error) {
	if region == "" {
		region = "us-central1"
	}
	if templateID == "" {
		templateID = "email-armor-guard"
	}

	endpoint := fmt.Sprintf("modelarmor.%s.rep.googleapis.com:443", region)
	client, err := modelarmor.NewRESTClient(ctx, option.WithEndpoint(endpoint))
	if err != nil {
		return nil, fmt.Errorf("failed creating model armor client: %w", err)
	}

	fullName := fmt.Sprintf("projects/%s/locations/%s/templates/%s", projectID, region, templateID)
	return &CloudModelArmorService{
		client:       client,
		templateName: fullName,
	}, nil
}

// Close closes the underlying gRPC / REST client.
func (s *CloudModelArmorService) Close() error {
	if s.client != nil {
		return s.client.Close()
	}
	return nil
}

// ScreenPrompt checks user/email input for prompt injections, jailbreaks, and harmful content.
func (s *CloudModelArmorService) ScreenPrompt(ctx context.Context, text string) (*SanitizeResult, error) {
	ctx, span := telemetry.StartToolSpan(ctx, "model_armor_sanitize",
		trace.WithAttributes(
			attribute.String("model_armor.template", s.templateName),
			attribute.Int("prompt.length", len(text)),
			attribute.String("gcp.agent.tool_call_args", fmt.Sprintf(`{"template": %q, "prompt_length": %d}`, s.templateName, len(text))),
		),
	)
	defer span.End()

	req := &modelarmorpb.SanitizeUserPromptRequest{
		Name: s.templateName,
		UserPromptData: &modelarmorpb.DataItem{
			DataItem: &modelarmorpb.DataItem_Text{
				Text: text,
			},
		},
	}

	resp, err := s.client.SanitizeUserPrompt(ctx, req)
	if err != nil {
		span.RecordError(err)
		slog.WarnContext(ctx, "[ModelArmor] SanitizeUserPrompt request failed", "error", err)
		// On unexpected API error, log error and allow execution to proceed gracefully
		res := &SanitizeResult{
			Passed:        true,
			BlockedReason: "",
		}
		span.SetAttributes(
			attribute.Bool("model_armor.passed", true),
			attribute.String("model_armor.error", err.Error()),
			attribute.String("gcp.agent.tool_response", fmt.Sprintf(`{"passed": true, "error": %q}`, err.Error())),
		)
		return res, nil
	}

	result := &SanitizeResult{
		Passed: true,
	}

	if resp != nil && resp.SanitizationResult != nil {
		sr := resp.SanitizationResult

		// 1. Inspect Prompt Injection and Jailbreak (Malicious payload)
		if pi, ok := sr.FilterResults["pi_and_jailbreak"]; ok && pi != nil {
			if piRes := pi.GetPiAndJailbreakFilterResult(); piRes != nil {
				if piRes.MatchState == modelarmorpb.FilterMatchState_MATCH_FOUND {
					result.Passed = false
					result.MatchFound = true
					result.JailbreakDetected = true
					result.Confidence = piRes.ConfidenceLevel.String()
					result.BlockedReason = "Prompt injection or jailbreak attempt detected by Model Armor"
				}
			}
		}

		// 2. Inspect Malicious URIs (Malicious payload)
		if uri, ok := sr.FilterResults["malicious_uris"]; ok && uri != nil {
			if uriRes := uri.GetMaliciousUriFilterResult(); uriRes != nil {
				if uriRes.MatchState == modelarmorpb.FilterMatchState_MATCH_FOUND {
					result.Passed = false
					result.MatchFound = true
					result.BlockedReason = "Malicious URI detected by Model Armor"
				}
			}
		}

		// 3. Inspect CSAM (Malicious payload)
		if csam, ok := sr.FilterResults["csam"]; ok && csam != nil {
			if csamRes := csam.GetCsamFilterFilterResult(); csamRes != nil {
				if csamRes.MatchState == modelarmorpb.FilterMatchState_MATCH_FOUND {
					result.Passed = false
					result.MatchFound = true
					result.BlockedReason = "Harmful content policy violation detected by Model Armor"
				}
			}
		}

		// 4. Inspect Sensitive Data Protection (SDP / PII)
		if sdp, ok := sr.FilterResults["sdp"]; ok && sdp != nil {
			if sdpRes := sdp.GetSdpFilterResult(); sdpRes != nil {
				if inspect := sdpRes.GetInspectResult(); inspect != nil {
					if inspect.MatchState == modelarmorpb.FilterMatchState_MATCH_FOUND {
						result.PIIFound = true
						for _, f := range inspect.Findings {
							result.InfoTypes = append(result.InfoTypes, f.InfoType)
						}
					}
				}
				if deid := sdpRes.GetDeidentifyResult(); deid != nil {
					if deid.MatchState == modelarmorpb.FilterMatchState_MATCH_FOUND {
						result.PIIFound = true
						result.InfoTypes = append(result.InfoTypes, deid.InfoTypes...)
						if deid.Data != nil && deid.Data.GetText() != "" {
							result.SanitizedText = deid.Data.GetText()
						}
					}
				}
			}
		}

		if !result.Passed && result.BlockedReason == "" {
			result.BlockedReason = "Content safety policy violation detected by Model Armor"
		}
	}

	// Always guarantee complete deterministic PII redaction across all content
	if result.SanitizedText == "" {
		result.SanitizedText = RedactPII(text)
	} else {
		result.SanitizedText = RedactPII(result.SanitizedText)
	}
	if result.SanitizedText != text {
		result.PIIFound = true
	}

	span.SetAttributes(
		attribute.Bool("model_armor.passed", result.Passed),
		attribute.Bool("model_armor.match_found", result.MatchFound),
		attribute.Bool("model_armor.jailbreak_detected", result.JailbreakDetected),
		attribute.Bool("model_armor.pii_found", result.PIIFound),
		attribute.String("model_armor.confidence", result.Confidence),
		attribute.String("model_armor.blocked_reason", result.BlockedReason),
		attribute.String("gcp.agent.tool_response", fmt.Sprintf(`{"passed": %t, "match_found": %t, "jailbreak_detected": %t, "pii_found": %t, "blocked_reason": %q}`, result.Passed, result.MatchFound, result.JailbreakDetected, result.PIIFound, result.BlockedReason)),
	)

	return result, nil
}

// MockModelArmorService simulates Model Armor for local tests and environments without GCP.
type MockModelArmorService struct{}

func NewMockModelArmorService() *MockModelArmorService {
	return &MockModelArmorService{}
}

func (m *MockModelArmorService) ScreenPrompt(ctx context.Context, text string) (*SanitizeResult, error) {
	ctx, span := telemetry.StartToolSpan(ctx, "model_armor_sanitize",
		trace.WithAttributes(
			attribute.String("model_armor.template", "mock-email-armor-guard"),
			attribute.Int("prompt.length", len(text)),
		),
	)
	defer span.End()

	lower := strings.ToLower(text)
	if strings.Contains(lower, "ignore previous instructions") ||
		strings.Contains(lower, "ignore all previous instructions") ||
		strings.Contains(lower, "system prompt override") ||
		strings.Contains(lower, "drop table") {
		res := &SanitizeResult{
			Passed:            false,
			MatchFound:        true,
			JailbreakDetected: true,
			Confidence:        "HIGH",
			BlockedReason:     "Prompt injection or jailbreak attempt detected by Model Armor",
			SanitizedText:     RedactPII(text),
		}
		span.SetAttributes(
			attribute.Bool("model_armor.passed", false),
			attribute.Bool("model_armor.match_found", true),
			attribute.Bool("model_armor.jailbreak_detected", true),
			attribute.String("model_armor.blocked_reason", res.BlockedReason),
			attribute.String("gcp.agent.tool_response", fmt.Sprintf(`{"passed": false, "jailbreak_detected": true, "blocked_reason": %q}`, res.BlockedReason)),
		)
		return res, nil
	}

	sanitized := RedactPII(text)
	piiDetected := sanitized != text
	var infoTypes []string
	if piiDetected {
		infoTypes = []string{"EMAIL_ADDRESS", "PHONE_NUMBER", "STREET_ADDRESS"}
	}

	span.SetAttributes(
		attribute.Bool("model_armor.passed", true),
		attribute.Bool("model_armor.match_found", false),
		attribute.Bool("model_armor.pii_found", piiDetected),
		attribute.String("gcp.agent.tool_response", fmt.Sprintf(`{"passed": true, "pii_found": %t}`, piiDetected)),
	)
	return &SanitizeResult{
		Passed:        true,
		MatchFound:    false,
		PIIFound:      piiDetected,
		InfoTypes:     infoTypes,
		SanitizedText: sanitized,
	}, nil
}

func (m *MockModelArmorService) Close() error {
	return nil
}
