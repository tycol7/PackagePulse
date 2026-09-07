package handlers_test

import (
	"context"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tylerdean/package-tracker-demo/internal/agent"
	"github.com/tylerdean/package-tracker-demo/internal/auth"
	"github.com/tylerdean/package-tracker-demo/internal/db"
	"github.com/tylerdean/package-tracker-demo/internal/handlers"
	"github.com/tylerdean/package-tracker-demo/internal/models"
	"github.com/tylerdean/package-tracker-demo/internal/templates"
)

func setupTestDigestHandler(t *testing.T) (*handlers.DigestHandler, db.Store) {
	tmpl, err := template.ParseFS(templates.FS, "*.html")
	if err != nil {
		t.Fatalf("Failed to parse templates: %v", err)
	}

	store := db.NewMemoryStore()
	mockAgent := agent.NewMockAgent()
	handler := handlers.NewDigestHandler(tmpl, store, mockAgent)
	return handler, store
}

func contextWithUser(userID, email, name string) context.Context {
	session := &auth.SessionPayload{
		UserID:      userID,
		Email:       email,
		DisplayName: name,
	}
	return context.WithValue(context.Background(), auth.UserContextKey, session)
}

func TestDigestHandler_ShowModal(t *testing.T) {
	handler, store := setupTestDigestHandler(t)
	ctx := contextWithUser("user_123", "someone@google.com", "Someone")

	_ = store.SaveUser(ctx, &models.User{
		ID:          "user_123",
		Email:       "someone@google.com",
		DisplayName: "Someone",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/digest/modal", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	handler.ShowModal(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got: %d", resp.StatusCode)
	}

	body := w.Body.String()
	if !strings.Contains(body, "Daily Package Digest (GenAI)") {
		t.Errorf("expected modal to contain title, got: %s", body)
	}
	if !strings.Contains(body, `hx-get="/api/digest/generate"`) {
		t.Errorf("expected modal to load preview via /api/digest/generate, got: %s", body)
	}
	if !strings.Contains(body, "spinner") {
		t.Errorf("expected modal to have spinner while loading, got: %s", body)
	}
	if strings.Contains(body, "Recipient:") || strings.Contains(body, "Recipients:") {
		t.Errorf("did not expect Recipient: in modal, got: %s", body)
	}
}

func TestDigestHandler_GeneratePreview(t *testing.T) {
	handler, store := setupTestDigestHandler(t)
	ctx := contextWithUser("user_123", "someone@google.com", "Someone")

	_ = store.SaveUser(ctx, &models.User{
		ID:          "user_123",
		Email:       "someone@google.com",
		DisplayName: "Someone",
	})

	_ = store.SavePackage(ctx, &models.Package{
		ID:             "pkg_1",
		UserID:         "user_123",
		Sender:         "Google Store",
		Carrier:        "FedEx",
		TrackingNumber: "12345",
		Status:         models.StatusOutForDelivery,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/digest/generate", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	handler.GeneratePreview(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got: %d", resp.StatusCode)
	}

	body := w.Body.String()
	if !strings.Contains(body, "Synthesized by") {
		t.Errorf("expected preview to show synthesizer badge, got: %s", body)
	}
	if strings.Contains(body, "Send Email") {
		t.Errorf("did not expect Send Email button in preview, got: %s", body)
	}
	if strings.Contains(body, "Recipient:") || strings.Contains(body, "Recipients:") {
		t.Errorf("did not expect Recipient in preview, got: %s", body)
	}
	if !strings.Contains(body, "Done") {
		t.Errorf("expected Done button in preview, got: %s", body)
	}
}

func TestDigestHandler_XSSProtection(t *testing.T) {
	handler, store := setupTestDigestHandler(t)
	ctx := contextWithUser("user_xss", "xss@google.com", "XSS Tester")

	_ = store.SaveUser(ctx, &models.User{
		ID:          "user_xss",
		Email:       "xss@google.com",
		DisplayName: "XSS Tester",
	})
	_ = store.SavePackage(ctx, &models.Package{
		ID:      "pkg_xss",
		UserID:  "user_xss",
		Sender:  "Mallory",
		Carrier: "USPS",
		Notes:   "<script>alert(1)</script><img src=x onerror=alert(2)>",
		Status:  models.StatusOutForDelivery,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/digest/generate", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	handler.GeneratePreview(w, req)
	body := w.Body.String()

	if strings.Contains(body, "<script>") || strings.Contains(body, "alert(1)") {
		t.Errorf("expected script tags to be sanitized, got: %s", body)
	}
	if strings.Contains(body, "onerror=") {
		t.Errorf("expected onerror event handler to be sanitized, got: %s", body)
	}
}


