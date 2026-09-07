package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tylerdean/package-tracker-demo/internal/auth"
	"github.com/tylerdean/package-tracker-demo/internal/models"
)

func TestService_SessionIssueAndValidation(t *testing.T) {
	svc := auth.NewService("client_id", "client_secret", "http://localhost:8080/auth/callback", "super-secret-test-key-32-bytes!!")

	user := &models.User{
		ID:          "sub_1029384756",
		Email:       "tylerdean@google.com",
		DisplayName: "Tyler Dean",
		AvatarURL:   "https://lh3.googleusercontent.com/avatar",
	}

	// Issue cookie
	rec := httptest.NewRecorder()
	if err := svc.IssueSessionCookie(rec, user); err != nil {
		t.Fatalf("failed issuing cookie: %v", err)
	}

	cookies := rec.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == auth.SessionCookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatalf("expected session cookie to be set")
	}

	// Validate valid cookie
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(sessionCookie)

	session, err := svc.ValidateSession(req)
	if err != nil {
		t.Fatalf("failed validating valid session: %v", err)
	}
	if session.UserID != user.ID || session.Email != user.Email {
		t.Errorf("session payload mismatch: got %+v", session)
	}

	// Test Tampered Cookie
	tamperedCookie := &http.Cookie{
		Name:  auth.SessionCookieName,
		Value: sessionCookie.Value + "tamper",
	}
	reqTampered := httptest.NewRequest("GET", "/", nil)
	reqTampered.AddCookie(tamperedCookie)

	_, err = svc.ValidateSession(reqTampered)
	if err == nil {
		t.Errorf("expected error when validating tampered session, got nil")
	}
}

func TestService_StateGeneration(t *testing.T) {
	svc := auth.NewService("client_id", "client_secret", "http://localhost:8080/auth/callback", "super-secret-test-key-32-bytes!!")
	rec := httptest.NewRecorder()

	url, err := svc.GetLoginURL(rec)
	if err != nil {
		t.Fatalf("failed getting login url: %v", err)
	}
	if url == "" {
		t.Errorf("expected non-empty login url")
	}

	// Check State Cookie was set
	cookies := rec.Result().Cookies()
	var stateCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == auth.StateCookieName {
			stateCookie = c
			break
		}
	}
	if stateCookie == nil {
		t.Errorf("expected state cookie to be set")
	}
}

func TestService_SessionExpiration(t *testing.T) {
	svc := auth.NewService("client_id", "client_secret", "http://localhost:8080/auth/callback", "super-secret-test-key-32-bytes!!")

	// Clear session
	rec := httptest.NewRecorder()
	svc.ClearSessionCookie(rec)

	cookies := rec.Result().Cookies()
	for _, c := range cookies {
		if c.Name == auth.SessionCookieName {
			if c.MaxAge != -1 || !c.Expires.Before(time.Now()) {
				t.Errorf("expected session cookie to be expired, got maxAge %d", c.MaxAge)
			}
		}
	}
}
