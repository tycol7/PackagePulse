package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/tylerdean/package-tracker-demo/internal/agent"
	"github.com/tylerdean/package-tracker-demo/internal/security"
	"github.com/tylerdean/package-tracker-demo/internal/storage"
	"github.com/tylerdean/package-tracker-demo/internal/telemetry"
)

// ReasoningEngineHandler serves the standard Agent Runtime contract routes
// (/api/reasoning_engine and /api/stream_reasoning_engine) for Google Cloud Agent Platform.
type ReasoningEngineHandler struct {
	agent      agent.LogisticsAgent
	modelArmor security.ModelArmorService
}

// NewReasoningEngineHandler creates a new handler.
func NewReasoningEngineHandler(aiAgent agent.LogisticsAgent, modelArmor security.ModelArmorService) *ReasoningEngineHandler {
	return &ReasoningEngineHandler{
		agent:      aiAgent,
		modelArmor: modelArmor,
	}
}

type reasoningEngineRequest struct {
	ClassMethod string                 `json:"class_method"`
	Input       map[string]interface{} `json:"input"`
}

type reasoningEngineResponse struct {
	Output interface{} `json:"output"`
}

// HandleQuery processes unary queries from Agent Runtime (:query).
func (h *ReasoningEngineHandler) HandleQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
	if err != nil {
		http.Error(w, "Failed reading request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var req reasoningEngineRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		req.Input = map[string]interface{}{"prompt": string(bodyBytes)}
	}

	prompt := extractPrompt(req.Input)
	projectID := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if projectID == "" {
		projectID = "package-tracker-demo"
	}

	sessionID := extractSessionID(req.Input)
	if sessionID == "" {
		sessionID = fmt.Sprintf("session_%d", time.Now().Unix())
	}

	ctx := telemetry.ExtractTraceContext(r)
	ctx = telemetry.ContextWithSessionID(ctx, sessionID)
	ctx, span := telemetry.StartAgentSpan(ctx, "invoke_agent AgentRuntime.Query",
		trace.WithAttributes(
			attribute.String("agent.class_method", req.ClassMethod),
			attribute.String("agent.prompt", prompt),
		),
	)
	defer span.End()

	telemetry.LogStep(ctx, projectID, "AgentRuntime", "Query", "PROCESSING", map[string]interface{}{
		"class_method": req.ClassMethod,
		"prompt":       prompt,
	})

	// Model Armor Prompt Screening
	if h.modelArmor != nil && prompt != "" {
		armorResult, err := h.modelArmor.ScreenPrompt(ctx, prompt)
		if err == nil && armorResult != nil && !armorResult.Passed {
			reason := armorResult.BlockedReason
			if reason == "" {
				reason = "Query blocked by Model Armor security policy."
			}
			span.SetAttributes(
				attribute.String("agent.status", "BLOCKED"),
				attribute.String("agent.blocked_reason", reason),
			)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error":              reason,
				"jailbreak_detected": armorResult.JailbreakDetected,
			})
			return
		}
	}

	output, err := h.dispatchQuery(ctx, req.Input, prompt)
	if err != nil {
		span.RecordError(err)
		log.Printf("[ReasoningEngine] Query failed: %v", err)
		http.Error(w, fmt.Sprintf("Agent execution failed: %v", err), http.StatusInternalServerError)
		return
	}

	span.SetAttributes(attribute.String("agent.status", "SUCCESS"))
	telemetry.LogStep(ctx, projectID, "AgentRuntime", "Query", "COMPLETED", map[string]interface{}{
		"status": "SUCCESS",
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(reasoningEngineResponse{Output: output})
}

// HandleStreamQuery processes streaming queries from Agent Runtime (:streamQuery).
func (h *ReasoningEngineHandler) HandleStreamQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
	if err != nil {
		http.Error(w, "Failed reading request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var req reasoningEngineRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		req.Input = map[string]interface{}{"prompt": string(bodyBytes)}
	}

	prompt := extractPrompt(req.Input)
	projectID := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if projectID == "" {
		projectID = "package-tracker-demo"
	}

	sessionID := extractSessionID(req.Input)
	if sessionID == "" {
		sessionID = fmt.Sprintf("session_%d", time.Now().Unix())
	}

	ctx := telemetry.ExtractTraceContext(r)
	ctx = telemetry.ContextWithSessionID(ctx, sessionID)
	ctx, span := telemetry.StartAgentSpan(ctx, "invoke_agent AgentRuntime.StreamQuery",
		trace.WithAttributes(
			attribute.String("agent.class_method", req.ClassMethod),
			attribute.String("agent.prompt", prompt),
		),
	)
	defer span.End()

	output, err := h.dispatchQuery(ctx, req.Input, prompt)
	if err != nil {
		span.RecordError(err)
		log.Printf("[ReasoningEngine] Stream query failed: %v", err)
		http.Error(w, fmt.Sprintf("Agent execution failed: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	// Agent Runtime expects newline-delimited JSON chunks
	line, _ := json.Marshal(reasoningEngineResponse{Output: output})
	_, _ = w.Write(append(line, '\n'))
}

func extractSessionID(input map[string]interface{}) string {
	for _, key := range []string{"session_id", "conversation_id", "session", "user_id"} {
		if v, ok := input[key]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func extractPrompt(input map[string]interface{}) string {
	for _, key := range []string{"prompt", "message", "query", "input", "text"} {
		if v, ok := input[key]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return "Hello"
}

func (h *ReasoningEngineHandler) dispatchQuery(ctx context.Context, input map[string]interface{}, prompt string) (interface{}, error) {
	// If input provides email subject/body for processing
	if emailVal, ok := input["email"]; ok {
		if emailStr, ok := emailVal.(string); ok && emailStr != "" {
			parsed := storage.ParsedEmail{
				Subject: "Inbound Email Query",
				From:    "sender@example.com",
				Body:    emailStr,
			}
			return h.agent.ProcessEmail(ctx, parsed)
		}
	}

	return h.agent.Query(ctx, prompt)
}
