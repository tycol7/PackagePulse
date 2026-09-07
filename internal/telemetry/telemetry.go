package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
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

	log.Printf("✅ Initialized OpenTelemetry OTLP Google Cloud Telemetry exporter for ReasoningEngine: %s, project: %s", agentEngineID, projectID)
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

// LogStep emits structured Google Cloud Logging JSON to stdout, correlated with Cloud Trace.
// Cloud Trace Explorer will automatically nest these log entries directly under the active span.
func LogStep(ctx context.Context, projectID, agentName, stepName, status string, payload map[string]interface{}) {
	if projectID == "" {
		projectID = os.Getenv("GOOGLE_CLOUD_PROJECT")
		if projectID == "" {
			projectID = "package-tracker-demo"
		}
	}

	span := trace.SpanFromContext(ctx)
	sc := span.SpanContext()

	entry := map[string]interface{}{
		"severity":   "INFO",
		"message":    fmt.Sprintf("[Agent Platform] [%s] %s: %s", agentName, stepName, status),
		"timestamp":  time.Now().UTC().Format(time.RFC3339Nano),
		"agent_name": agentName,
		"step":       stepName,
		"status":     status,
	}

	if sc.IsValid() {
		entry["logging.googleapis.com/trace"] = fmt.Sprintf("projects/%s/traces/%s", projectID, sc.TraceID().String())
		entry["logging.googleapis.com/spanId"] = sc.SpanID().String()
		entry["logging.googleapis.com/trace_sampled"] = sc.IsSampled()
	}

	for k, v := range payload {
		entry[k] = v
	}

	data, err := json.Marshal(entry)
	if err == nil {
		fmt.Println(string(data))
	}
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


