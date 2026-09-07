package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace"
)

func TestInitLogger_ProducesGCPStructuredJSON(t *testing.T) {
	var buf bytes.Buffer
	logger := InitLoggerWithWriter(&buf, "package-tracker-test")

	ctx := context.Background()
	logger.InfoContext(ctx, "PackagePulse service initialized", "port", "8080")

	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatalf("expected log output, got empty string")
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(line), &parsed); err != nil {
		t.Fatalf("failed to parse log output as JSON: %v, raw output: %s", err, line)
	}

	if parsed["severity"] != "INFO" {
		t.Errorf("expected severity 'INFO', got '%v'", parsed["severity"])
	}
	if parsed["message"] != "PackagePulse service initialized" {
		t.Errorf("expected message 'PackagePulse service initialized', got '%v'", parsed["message"])
	}
	if parsed["port"] != "8080" {
		t.Errorf("expected port '8080', got '%v'", parsed["port"])
	}
	if ts, ok := parsed["timestamp"].(string); !ok || ts == "" {
		t.Errorf("expected valid timestamp string, got '%v'", parsed["timestamp"])
	} else {
		if _, err := time.Parse(time.RFC3339Nano, ts); err != nil {
			t.Errorf("timestamp is not valid RFC3339Nano: %v", err)
		}
	}
}

func TestLogger_CloudTraceCorrelation(t *testing.T) {
	var buf bytes.Buffer
	logger := InitLoggerWithWriter(&buf, "demo-project-id")

	traceID, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	spanID, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)
	ctx = ContextWithSessionID(ctx, "session-conv-987")

	logger.InfoContext(ctx, "Processing logistics query")

	line := strings.TrimSpace(buf.String())
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(line), &parsed); err != nil {
		t.Fatalf("failed parsing JSON: %v", err)
	}

	expectedTrace := "projects/demo-project-id/traces/4bf92f3577b34da6a3ce929d0e0e4736"
	if parsed["logging.googleapis.com/trace"] != expectedTrace {
		t.Errorf("expected trace '%s', got '%v'", expectedTrace, parsed["logging.googleapis.com/trace"])
	}
	if parsed["logging.googleapis.com/spanId"] != "00f067aa0ba902b7" {
		t.Errorf("expected spanId '00f067aa0ba902b7', got '%v'", parsed["logging.googleapis.com/spanId"])
	}
	if parsed["logging.googleapis.com/trace_sampled"] != true {
		t.Errorf("expected trace_sampled true, got '%v'", parsed["logging.googleapis.com/trace_sampled"])
	}
	if parsed["gen_ai.conversation.id"] != "session-conv-987" {
		t.Errorf("expected gen_ai.conversation.id 'session-conv-987', got '%v'", parsed["gen_ai.conversation.id"])
	}
}

func TestLogger_AutomaticPIIRedaction(t *testing.T) {
	var buf bytes.Buffer
	logger := InitLoggerWithWriter(&buf, "demo-project-id")
	ctx := context.Background()

	logger.InfoContext(ctx, "Inbound email from user john.doe@example.com with phone 415-555-2671",
		"customer_address", "1600 Amphitheatre Pkwy, Mountain View, CA 94043",
		"auth_header", "Bearer secret-token-that-is-longer-than-20-chars",
		"credit_card", "4111 1111 1111 1111",
		"ssn", "000-12-3456",
	)

	line := strings.TrimSpace(buf.String())
	if strings.Contains(line, "john.doe@example.com") {
		t.Errorf("found unredacted email in log: %s", line)
	}
	if strings.Contains(line, "415-555-2671") {
		t.Errorf("found unredacted phone in log: %s", line)
	}
	if strings.Contains(line, "1600 Amphitheatre Pkwy") {
		t.Errorf("found unredacted address in log: %s", line)
	}
	if strings.Contains(line, "secret-token-that-is-longer-than-20-chars") {
		t.Errorf("found unredacted secret in log: %s", line)
	}
	if strings.Contains(line, "4111 1111 1111 1111") {
		t.Errorf("found unredacted credit card in log: %s", line)
	}
	if strings.Contains(line, "000-12-3456") {
		t.Errorf("found unredacted SSN in log: %s", line)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(line), &parsed); err != nil {
		t.Fatalf("failed to unmarshal log: %v", err)
	}

	if parsed["customer_address"] != "[ADDRESS_REDACTED]" {
		t.Errorf("customer_address not redacted: got %v", parsed["customer_address"])
	}
	if parsed["auth_header"] != "[SECRET_REDACTED]" {
		t.Errorf("auth_header not redacted: got %v", parsed["auth_header"])
	}
}

func TestLogStep_UsesSlog(t *testing.T) {
	var buf bytes.Buffer
	_ = InitLoggerWithWriter(&buf, "demo-project-id")

	traceID, _ := trace.TraceIDFromHex("1234567890abcdef1234567890abcdef")
	spanID, _ := trace.SpanIDFromHex("1234567890abcdef")
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	payload := map[string]interface{}{
		"tracking_number": "1Z9999999999999999",
		"carrier":         "UPS",
		"customer_email":  "customer@example.org",
	}

	LogStep(ctx, "demo-project-id", "Reconciler", "reconciliation", "In Transit", payload)

	line := strings.TrimSpace(buf.String())
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(line), &parsed); err != nil {
		t.Fatalf("failed parsing log JSON: %v, raw: %s", err, line)
	}

	if parsed["agent_name"] != "Reconciler" {
		t.Errorf("expected agent_name 'Reconciler', got '%v'", parsed["agent_name"])
	}
	if parsed["step"] != "reconciliation" {
		t.Errorf("expected step 'reconciliation', got '%v'", parsed["step"])
	}
	if parsed["status"] != "In Transit" {
		t.Errorf("expected status 'In Transit', got '%v'", parsed["status"])
	}
	if parsed["tracking_number"] != "1Z9999999999999999" {
		t.Errorf("expected tracking number preserved, got '%v'", parsed["tracking_number"])
	}
	if parsed["customer_email"] != "[EMAIL_REDACTED]" {
		t.Errorf("expected customer_email to be redacted, got '%v'", parsed["customer_email"])
	}
	if parsed["severity"] != "INFO" {
		t.Errorf("expected severity 'INFO', got '%v'", parsed["severity"])
	}
	if !strings.Contains(parsed["message"].(string), "[Agent Platform] [Reconciler] reconciliation: In Transit") {
		t.Errorf("expected message pattern, got '%v'", parsed["message"])
	}
	if !strings.Contains(parsed["logging.googleapis.com/trace"].(string), "1234567890abcdef1234567890abcdef") {
		t.Errorf("expected trace correlation, got '%v'", parsed["logging.googleapis.com/trace"])
	}
}
