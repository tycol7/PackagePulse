package handlers

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"regexp"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/tylerdean/package-tracker-demo/internal/agent"
	"github.com/tylerdean/package-tracker-demo/internal/auth"
	"github.com/tylerdean/package-tracker-demo/internal/db"
	"github.com/tylerdean/package-tracker-demo/internal/models"
	"github.com/tylerdean/package-tracker-demo/internal/telemetry"
)

type DigestHandler struct {
	tmpl  *template.Template
	store db.Store
	agent agent.LogisticsAgent
}

func NewDigestHandler(tmpl *template.Template, store db.Store, ag agent.LogisticsAgent) *DigestHandler {
	return &DigestHandler{
		tmpl:  tmpl,
		store: store,
		agent: ag,
	}
}

// ShowModal immediately renders the digest modal with a loading spinner while the preview is synthesized.
func (h *DigestHandler) ShowModal(w http.ResponseWriter, r *http.Request) {
	session := auth.GetUserFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	user, err := h.store.GetUser(r.Context(), session.UserID)
	if err != nil || user == nil {
		user = &models.User{
			ID:          session.UserID,
			DisplayName: session.DisplayName,
			Email:       session.Email,
		}
	}

	data := map[string]interface{}{
		"User": user,
	}

	_ = h.tmpl.ExecuteTemplate(w, "digest_modal", data)
}

// GeneratePreview synthesizes the daily GenAI digest briefing and renders the preview with the Send Email button.
func (h *DigestHandler) GeneratePreview(w http.ResponseWriter, r *http.Request) {
	session := auth.GetUserFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	user, err := h.store.GetUser(r.Context(), session.UserID)
	if err != nil || user == nil {
		user = &models.User{
			ID:          session.UserID,
			DisplayName: session.DisplayName,
			Email:       session.Email,
			Preferences: models.UserPreferences{
				Timezone: "America/Los_Angeles",
			},
		}
	}

	// Determine local date in user's timezone
	loc, err := time.LoadLocation(user.Preferences.Timezone)
	if err != nil {
		loc = time.UTC
	}
	todayStr := time.Now().In(loc).Format("2006-01-02")

	// Get packages arriving today
	packages, err := h.store.ListPackagesArrivingToday(r.Context(), session.UserID, todayStr)
	if err != nil || len(packages) == 0 {
		// If none marked arriving today, include active packages (In Transit / Out for Delivery) so demo shows something
		allActive, _ := h.store.ListPackages(r.Context(), session.UserID, "")
		for _, p := range allActive {
			if p.Status == models.StatusOutForDelivery || p.Status == models.StatusInTransit {
				packages = append(packages, p)
			}
		}
	}

	// Synthesize GenAI digest briefing
	sessionID := fmt.Sprintf("session-digest-%s", session.UserID)
	ctx := telemetry.ExtractTraceContext(r)
	ctx = telemetry.ContextWithSessionID(ctx, sessionID)
	ctx, span := telemetry.StartAgentSpan(ctx, "invoke_agent DigestAgent",
		trace.WithAttributes(
			attribute.String("user.id", session.UserID),
			attribute.Int("package.count", len(packages)),
		),
	)
	defer span.End()

	htmlContent, err := h.agent.ComposeDigest(ctx, user, packages)
	if err != nil {
		span.RecordError(err)
		log.Printf("[Digest] Error composing digest: %v", err)
		data := map[string]interface{}{
			"User":  user,
			"Error": err.Error(),
		}
		_ = h.tmpl.ExecuteTemplate(w, "digest_error", data)
		return
	}
	span.SetAttributes(attribute.String("agent.status", "SUCCESS"))

	data := map[string]interface{}{
		"User":         user,
		"HTMLContent":  template.HTML(sanitizeDigestHTML(htmlContent)),
		"PackageCount": len(packages),
		"GeneratedAt":  time.Now().In(loc).Format("3:04 PM MST"),
	}

	_ = h.tmpl.ExecuteTemplate(w, "digest_preview", data)
}


// PreviewDigest maintains backward compatibility by opening the digest modal.
func (h *DigestHandler) PreviewDigest(w http.ResponseWriter, r *http.Request) {
	h.ShowModal(w, r)
}


var (
	scriptRegex    = regexp.MustCompile(`(?i)<script[\s\S]*?</script>`)
	iframeRegex    = regexp.MustCompile(`(?i)<iframe[\s\S]*?</iframe>`)
	objectRegex    = regexp.MustCompile(`(?i)<(object|embed|applet)[\s\S]*?</(object|embed|applet)>`)
	eventAttrRegex = regexp.MustCompile(`(?i)\s+on\w+\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)`)
	jsLinkRegex    = regexp.MustCompile(`(?i)href\s*=\s*["']\s*javascript:[^"']*["']`)
)

func sanitizeDigestHTML(input string) string {
	cleaned := scriptRegex.ReplaceAllString(input, "")
	cleaned = iframeRegex.ReplaceAllString(cleaned, "")
	cleaned = objectRegex.ReplaceAllString(cleaned, "")
	cleaned = eventAttrRegex.ReplaceAllString(cleaned, "")
	cleaned = jsLinkRegex.ReplaceAllString(cleaned, `href="#"`)
	return cleaned
}
