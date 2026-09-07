package telemetry

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const (
	TraceHeaderKey        = "X-Cloud-Trace-Context"
	TracerName            = "package-tracker/agent"
	DefaultAgentEngineID  = "4763811698467405824"
	DefaultProjectNumber  = "336526219121"
	DefaultRegion         = "us-central1"
)

// InitTracer configures OpenTelemetry to export spans via OTLP directly to Google Cloud Telemetry API (telemetry.googleapis.com).
func InitTracer(ctx context.Context, projectID string) (func(context.Context) error, error) {
	if projectID == "" {
		projectID = os.Getenv("PROJECT_ID")
	}
	if projectID == "" {
		projectID = os.Getenv("GOOGLE_CLOUD_PROJECT")
	}
	if projectID == "" {
		projectID = os.Getenv("GCP_PROJECT")
	}
	if projectID == "" {
		projectID = "package-tracker-demo"
	}

	agentEngineID := os.Getenv("AGENT_ENGINE_ID")
	if agentEngineID == "" {
		agentEngineID = DefaultAgentEngineID
	}
	projectNumber := os.Getenv("PROJECT_NUMBER")
	if projectNumber == "" {
		projectNumber = DefaultProjectNumber
	}
	region := os.Getenv("REGION")
	if region == "" {
		region = DefaultRegion
	}

	creds, err := google.FindDefaultCredentials(ctx, "https://www.googleapis.com/auth/cloud-platform")
	var httpClient *http.Client
	if err == nil && creds != nil {
		httpClient = oauth2.NewClient(ctx, creds.TokenSource)
	} else {
		// Fallback for local development if gcloud CLI token is present
		tokenBytes, gerr := exec.Command("gcloud", "auth", "print-access-token").Output()
		if gerr == nil && len(tokenBytes) > 0 {
			ts := oauth2.StaticTokenSource(&oauth2.Token{
				AccessToken: strings.TrimSpace(string(tokenBytes)),
			})
			httpClient = oauth2.NewClient(ctx, ts)
		} else {
			httpClient = http.DefaultClient
		}
	}

	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithHTTPClient(httpClient),
		otlptracehttp.WithEndpointURL("https://telemetry.googleapis.com/v1/traces"),
		otlptracehttp.WithHeaders(map[string]string{
			"x-goog-user-project": projectID,
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create OTLP trace exporter: %w", err)
	}

	resourceURI := fmt.Sprintf("//aiplatform.googleapis.com/projects/%s/locations/%s/reasoningEngines/%s", projectNumber, region, agentEngineID)

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(agentEngineID),
			attribute.String("service.name", agentEngineID),
			attribute.String("cloud.platform", "gcp.agent_engine"),
			attribute.String("cloud.provider", "gcp"),
			attribute.String("cloud.region", region),
			attribute.String("cloud.resource_id", resourceURI),
			attribute.String("cloud.resource.id", resourceURI),
			attribute.String("gcp.project_id", projectID),
		),
	)
	if err != nil {
		res = resource.Default()
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter,
			sdktrace.WithBatchTimeout(500*time.Millisecond),
			sdktrace.WithExportTimeout(5*time.Second),
			sdktrace.WithMaxExportBatchSize(64),
		),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithResource(res),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	slog.Info("✅ Initialized OpenTelemetry OTLP Google Cloud Telemetry exporter",
		"reasoning_engine_id", agentEngineID,
		"project_id", projectID,
	)
	return tp.Shutdown, nil
}

// Tracer returns the named tracer for agent operations.
func Tracer() trace.Tracer {
	return otel.GetTracerProvider().Tracer(TracerName)
}

// ExtractTraceContext extracts trace & span context from incoming HTTP headers (W3C or Cloud Run X-Cloud-Trace-Context).
func ExtractTraceContext(r *http.Request) context.Context {
	ctx := r.Context()
	if r == nil {
		return ctx
	}

	// 1. Try standard W3C traceparent
	prop := otel.GetTextMapPropagator()
	if prop != nil {
		ctx = prop.Extract(ctx, propagation.HeaderCarrier(r.Header))
	}

	// 2. If no valid span context found, try Google Cloud Run's X-Cloud-Trace-Context
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		header := r.Header.Get(TraceHeaderKey)
		if header != "" {
			parts := strings.Split(header, "/")
			if len(parts) >= 1 && len(parts[0]) == 32 {
				traceID, err := trace.TraceIDFromHex(parts[0])
				if err == nil {
					var spanID trace.SpanID
					if len(parts) >= 2 {
						spanParts := strings.Split(parts[1], ";")
						spanID, _ = trace.SpanIDFromHex(spanParts[0])
					}
					scc := trace.NewSpanContext(trace.SpanContextConfig{
						TraceID:    traceID,
						SpanID:     spanID,
						TraceFlags: trace.FlagsSampled,
						Remote:     true,
					})
					ctx = trace.ContextWithRemoteSpanContext(ctx, scc)
				}
			}
		}
	}

	return ctx
}

var (
	emailRegex          = regexp.MustCompile(`(?i)\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`)
	phoneFormattedRegex = regexp.MustCompile(`(?i)\b(?:\+?1[-.\s]?)?(?:\([2-9]\d{2}\)|[2-9]\d{2})[-.\s][2-9]\d{2}[-.\s]\d{4}\b`)
	phoneParenRegex     = regexp.MustCompile(`\([2-9]\d{2}\)\s*[2-9]\d{2}[-.\s]?\d{4}\b`)
	ssnRegex            = regexp.MustCompile(`\b[0-9]{3}-[0-9]{2}-[0-9]{4}\b`)
	creditCardRegex     = regexp.MustCompile(`\b(?:4[0-9]{3}[ -][0-9]{4}[ -][0-9]{4}[ -][0-9]{4}|5[1-5][0-9]{2}[ -][0-9]{4}[ -][0-9]{4}[ -][0-9]{4}|3[47][0-9]{2}[ -][0-9]{6}[ -][0-9]{5}|6(?:011|5[0-9]{2})[ -][0-9]{4}[ -][0-9]{4}[ -][0-9]{4})\b`)
	streetSuffixes      = `(?:Street|St\.?|Avenue|Ave\.?|Road|Rd\.?|Boulevard|Blvd\.?|Drive|Dr\.?|Lane|Ln\.?|Way|Court|Ct\.?|Circle|Cir\.?|Place|Pl\.?|Terrace|Ter\.?|Parkway|Pkwy\.?|Suite|Ste\.?|Apt\.?)`
	addressRegex        = regexp.MustCompile(`(?i)\b\d{1,5}\s+[A-Za-z0-9\.\s,]{2,30}\s+` + streetSuffixes + `\b(?:[,\s]+[A-Za-z\s]+[,\s]+[A-Z]{2}\s+\d{5}(?:-\d{4})?)?`)
	secretRegex         = regexp.MustCompile(`(?i)\b(bearer\s+[a-zA-Z0-9_\-\.]{20,}|ghp_[a-zA-Z0-9]{36}|AIza[0-9A-Za-z-_]{35})\b`)
)

// RedactPII scrubs sensitive personal identifiable information (PII) including emails, phones, addresses, SSNs, credit cards, and secrets.
func RedactPII(text string) string {
	if text == "" {
		return ""
	}
	res := secretRegex.ReplaceAllString(text, "[SECRET_REDACTED]")
	res = creditCardRegex.ReplaceAllString(res, "[CREDENTIAL_REDACTED]")
	res = ssnRegex.ReplaceAllString(res, "[SSN_REDACTED]")
	res = addressRegex.ReplaceAllString(res, "[ADDRESS_REDACTED]")
	res = phoneFormattedRegex.ReplaceAllString(res, "[PHONE_REDACTED]")
	res = phoneParenRegex.ReplaceAllString(res, "[PHONE_REDACTED]")
	res = emailRegex.ReplaceAllString(res, "[EMAIL_REDACTED]")
	return res
}

// SanitizeLogMap recursively scrubs all string values in a telemetry payload map to prevent PII leakage.
func SanitizeLogMap(m map[string]interface{}) map[string]interface{} {
	if m == nil {
		return nil
	}
	clean := make(map[string]interface{}, len(m))
	for k, v := range m {
		switch val := v.(type) {
		case string:
			clean[k] = RedactPII(val)
		case map[string]interface{}:
			clean[k] = SanitizeLogMap(val)
		case []interface{}:
			cleanArr := make([]interface{}, len(val))
			for i, item := range val {
				if strItem, ok := item.(string); ok {
					cleanArr[i] = RedactPII(strItem)
				} else if mapItem, ok := item.(map[string]interface{}); ok {
					cleanArr[i] = SanitizeLogMap(mapItem)
				} else {
					cleanArr[i] = item
				}
			}
			clean[k] = cleanArr
		default:
			clean[k] = v
		}
	}
	return clean
}

// GCPHandler is a dedicated slog.Handler that enriches records for Google Cloud Logging:
// 1. Injects Cloud Trace correlation ("logging.googleapis.com/trace", "logging.googleapis.com/spanId", "logging.googleapis.com/trace_sampled")
//    from OpenTelemetry span context in context.Context.
// 2. Injects Generative AI session ID ("gen_ai.conversation.id") when present in context.Context.
// 3. Formats severity, timestamp, and message keys according to GCP Cloud Logging specification.
// 4. Automatically scrubs PII from all logged strings, messages, and payloads via RedactPII and SanitizeLogMap.
type GCPHandler struct {
	next      slog.Handler
	projectID string
}

// NewGCPHandler creates a new GCP-compliant slog.Handler writing structured JSON to w.
func NewGCPHandler(w io.Writer, projectID string) *GCPHandler {
	if projectID == "" {
		projectID = os.Getenv("GOOGLE_CLOUD_PROJECT")
		if projectID == "" {
			projectID = "package-tracker-demo"
		}
	}

	opts := &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			// Map slog Level to GCP Cloud Logging severity string
			if a.Key == slog.LevelKey {
				level, ok := a.Value.Any().(slog.Level)
				if !ok {
					return slog.String("severity", "INFO")
				}
				var sev string
				switch {
				case level < slog.LevelInfo:
					sev = "DEBUG"
				case level < slog.LevelWarn:
					sev = "INFO"
				case level < slog.LevelError:
					sev = "WARNING"
				default:
					sev = "ERROR"
				}
				return slog.String("severity", sev)
			}
			// Map msg to message and sanitize PII
			if a.Key == slog.MessageKey {
				return slog.String("message", RedactPII(a.Value.String()))
			}
			// Map time to timestamp in RFC3339Nano UTC
			if a.Key == slog.TimeKey {
				return slog.String("timestamp", a.Value.Time().UTC().Format(time.RFC3339Nano))
			}
			// Automatically scrub PII from any string attribute
			if a.Value.Kind() == slog.KindString {
				return slog.String(a.Key, RedactPII(a.Value.String()))
			}
			if a.Value.Kind() == slog.KindAny {
				switch val := a.Value.Any().(type) {
				case string:
					return slog.String(a.Key, RedactPII(val))
				case map[string]interface{}:
					return slog.Any(a.Key, SanitizeLogMap(val))
				}
			}
			return a
		},
	}

	baseHandler := slog.NewJSONHandler(w, opts)
	return &GCPHandler{
		next:      baseHandler,
		projectID: projectID,
	}
}

func (h *GCPHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *GCPHandler) Handle(ctx context.Context, r slog.Record) error {
	projectID := h.projectID
	if projectID == "" {
		projectID = os.Getenv("GOOGLE_CLOUD_PROJECT")
		if projectID == "" {
			projectID = "package-tracker-demo"
		}
	}

	// Enrich with OpenTelemetry Cloud Trace correlation
	span := trace.SpanFromContext(ctx)
	sc := span.SpanContext()
	if sc.IsValid() {
		r.AddAttrs(
			slog.String("logging.googleapis.com/trace", fmt.Sprintf("projects/%s/traces/%s", projectID, sc.TraceID().String())),
			slog.String("logging.googleapis.com/spanId", sc.SpanID().String()),
			slog.Bool("logging.googleapis.com/trace_sampled", sc.IsSampled()),
		)
	}

	sessionID := SessionIDFromContext(ctx)
	if sessionID != "" {
		r.AddAttrs(slog.String("gen_ai.conversation.id", sessionID))
	}

	return h.next.Handle(ctx, r)
}

func (h *GCPHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &GCPHandler{
		next:      h.next.WithAttrs(attrs),
		projectID: h.projectID,
	}
}

func (h *GCPHandler) WithGroup(name string) slog.Handler {
	return &GCPHandler{
		next:      h.next.WithGroup(name),
		projectID: h.projectID,
	}
}

var (
	globalLogger   *slog.Logger
	globalLoggerMu sync.RWMutex
)

// InitLogger configures and sets the default slog.Logger formatted for Google Cloud Logging with trace correlation and PII redaction.
func InitLogger(projectID string) *slog.Logger {
	return InitLoggerWithWriter(os.Stdout, projectID)
}

// InitLoggerWithWriter allows configuring structured slog logging to an arbitrary writer (useful for unit tests and verification).
func InitLoggerWithWriter(w io.Writer, projectID string) *slog.Logger {
	handler := NewGCPHandler(w, projectID)
	logger := slog.New(handler)
	globalLoggerMu.Lock()
	globalLogger = logger
	globalLoggerMu.Unlock()
	slog.SetDefault(logger)
	return logger
}

// Logger returns the configured slog.Logger instance, initializing with defaults if not already configured.
func Logger() *slog.Logger {
	globalLoggerMu.RLock()
	l := globalLogger
	globalLoggerMu.RUnlock()
	if l != nil {
		return l
	}
	return InitLogger(os.Getenv("GOOGLE_CLOUD_PROJECT"))
}

// InfoContext logs an informational message via slog with Cloud Trace correlation and PII scrubbing.
func InfoContext(ctx context.Context, msg string, args ...any) {
	Logger().InfoContext(ctx, msg, args...)
}

// WarnContext logs a warning message via slog with Cloud Trace correlation and PII scrubbing.
func WarnContext(ctx context.Context, msg string, args ...any) {
	Logger().WarnContext(ctx, msg, args...)
}

// ErrorContext logs an error message via slog with Cloud Trace correlation and PII scrubbing.
func ErrorContext(ctx context.Context, msg string, args ...any) {
	Logger().ErrorContext(ctx, msg, args...)
}

// DebugContext logs a debug message via slog with Cloud Trace correlation and PII scrubbing.
func DebugContext(ctx context.Context, msg string, args ...any) {
	Logger().DebugContext(ctx, msg, args...)
}

// LogStep emits structured Google Cloud Logging JSON via dedicated log/slog, correlated with Cloud Trace.
// All payload fields and messages are automatically scrubbed of PII.
func LogStep(ctx context.Context, projectID, agentName, stepName, status string, payload map[string]interface{}) {
	if projectID == "" {
		projectID = os.Getenv("GOOGLE_CLOUD_PROJECT")
		if projectID == "" {
			projectID = "package-tracker-demo"
		}
	}

	attrs := []slog.Attr{
		slog.String("agent_name", agentName),
		slog.String("step", stepName),
		slog.String("status", status),
	}

	sanitizedPayload := SanitizeLogMap(payload)
	for k, v := range sanitizedPayload {
		attrs = append(attrs, slog.Any(k, v))
	}

	msg := fmt.Sprintf("[Agent Platform] [%s] %s: %s", agentName, stepName, status)
	Logger().LogAttrs(ctx, slog.LevelInfo, msg, attrs...)
}

type contextKey string

const sessionIDKey contextKey = "gen_ai.conversation.id"

// ContextWithSessionID stores a session/conversation ID in the context.
func ContextWithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, sessionIDKey, sessionID)
}

// SessionIDFromContext retrieves the session/conversation ID from context.
func SessionIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(sessionIDKey).(string); ok && v != "" {
		return v
	}
	return ""
}

func withSessionAttr(ctx context.Context, opts []trace.SpanStartOption) []trace.SpanStartOption {
	sessionID := SessionIDFromContext(ctx)
	if sessionID != "" {
		opts = append(opts, trace.WithAttributes(attribute.String("gen_ai.conversation.id", sessionID)))
	}
	return opts
}

// StartWorkflowSpan starts a root span with gen_ai.operation.name=invoke_workflow
func StartWorkflowSpan(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	if !strings.HasPrefix(name, "invoke_workflow ") && name != "invoke_workflow" {
		name = "invoke_workflow " + name
	}
	opts = append(opts, trace.WithAttributes(attribute.String("gen_ai.operation.name", "invoke_workflow")))
	opts = withSessionAttr(ctx, opts)
	return Tracer().Start(ctx, name, opts...)
}

// StartAgentSpan starts a span with gen_ai.operation.name=invoke_agent
func StartAgentSpan(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	if !strings.HasPrefix(name, "invoke_agent ") && name != "invoke_agent" {
		name = "invoke_agent " + name
	}
	opts = append(opts, trace.WithAttributes(attribute.String("gen_ai.operation.name", "invoke_agent")))
	opts = withSessionAttr(ctx, opts)
	return Tracer().Start(ctx, name, opts...)
}

// StartToolSpan starts a span with gen_ai.operation.name=execute_tool
func StartToolSpan(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	if !strings.HasPrefix(name, "execute_tool ") && name != "execute_tool" {
		name = "execute_tool " + name
	}
	opts = append(opts, trace.WithAttributes(attribute.String("gen_ai.operation.name", "execute_tool")))
	opts = withSessionAttr(ctx, opts)
	return Tracer().Start(ctx, name, opts...)
}

// StartLLMSpan starts a span with gen_ai.operation.name=generate_content
func StartLLMSpan(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	opts = append(opts, trace.WithAttributes(attribute.String("gen_ai.operation.name", "generate_content")))
	opts = withSessionAttr(ctx, opts)
	return Tracer().Start(ctx, name, opts...)
}


