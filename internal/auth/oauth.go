package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tylerdean/package-tracker-demo/internal/models"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/idtoken"
)

const (
	SessionCookieName = "pt_session"
	StateCookieName   = "oauth_state"
	SessionDuration   = 7 * 24 * time.Hour
)

type SessionPayload struct {
	UserID      string    `json:"user_id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
	AvatarURL   string    `json:"avatar_url"`
	IssuedAt    time.Time `json:"issued_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type Service struct {
	clientID      string
	clientSecret  string
	redirectURL   string
	sessionSecret []byte
	oauthConfig   *oauth2.Config
}

func NewService(clientID, clientSecret, redirectURL, sessionSecret string) *Service {
	cfg := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		Scopes: []string{
			"openid",
			"https://www.googleapis.com/auth/userinfo.email",
			"https://www.googleapis.com/auth/userinfo.profile",
		},
		Endpoint: google.Endpoint,
	}

	return &Service{
		clientID:      clientID,
		clientSecret:  clientSecret,
		redirectURL:   redirectURL,
		sessionSecret: []byte(sessionSecret),
		oauthConfig:   cfg,
	}
}

// GetLoginURL generates the Google OAuth URL with CSRF state and hosted domain steering.
func (s *Service) GetLoginURL(w http.ResponseWriter) (string, error) {
	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		return "", err
	}
	state := base64.URLEncoding.EncodeToString(stateBytes)

	// Set secure state cookie
	http.SetCookie(w, &http.Cookie{
		Name:     StateCookieName,
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		Secure:   strings.HasPrefix(s.redirectURL, "https://"),
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(10 * time.Minute),
	})

	url := s.oauthConfig.AuthCodeURL(
		state,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("hd", "google.com"),
		oauth2.SetAuthURLParam("prompt", "select_account"),
	)
	return url, nil
}

// ExchangeAndValidate processes the callback, verifies CSRF, exchanges code for ID Token, and checks @google.com.
func (s *Service) ExchangeAndValidate(ctx context.Context, r *http.Request) (*models.User, error) {
	// 1. Verify CSRF State
	stateCookie, err := r.Cookie(StateCookieName)
	if err != nil || stateCookie.Value == "" {
		return nil, errors.New("missing oauth state cookie")
	}
	queryState := r.URL.Query().Get("state")
	if queryState == "" || queryState != stateCookie.Value {
		return nil, errors.New("invalid or mismatched oauth state")
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		return nil, errors.New("missing authorization code")
	}

	// 2. Exchange authorization code with accounts.google.com
	token, err := s.oauthConfig.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("token exchange failed: %w", err)
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return nil, errors.New("no id_token returned in oauth response")
	}

	// 3. Cryptographic validation of ID token against Google's public keys
	payload, err := idtoken.Validate(ctx, rawIDToken, s.clientID)
	if err != nil {
		return nil, fmt.Errorf("cryptographic id_token validation failed: %w", err)
	}

	// 4. Validate @google.com domain
	email, _ := payload.Claims["email"].(string)
	hd, _ := payload.Claims["hd"].(string)

	if !isGoogleEmail(email, hd) {
		return nil, fmt.Errorf("access restricted: %s is not an authorized @google.com account", email)
	}

	name, _ := payload.Claims["name"].(string)
	if name == "" {
		name = email
	}
	picture, _ := payload.Claims["picture"].(string)

	user := &models.User{
		ID:          payload.Subject,
		Email:       email,
		DisplayName: name,
		AvatarURL:   picture,
		NominalForwardingAddress: fmt.Sprintf("track+%s@packagepulse.google.com", payload.Subject),
		Preferences: models.UserPreferences{
			DailyDigestEnabled: true,
			DigestTimeLocal:    "08:00",
			Timezone:           "America/Los_Angeles",
			RecipientEmails:    []string{email},
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	return user, nil
}

func isGoogleEmail(email, hd string) bool {
	if hd == "google.com" {
		return true
	}
	return strings.HasSuffix(strings.ToLower(email), "@google.com")
}

// IssueSessionCookie encodes, signs, and sets the session cookie on the response.
func (s *Service) IssueSessionCookie(w http.ResponseWriter, user *models.User) error {
	session := SessionPayload{
		UserID:      user.ID,
		Email:       user.Email,
		DisplayName: user.DisplayName,
		AvatarURL:   user.AvatarURL,
		IssuedAt:    time.Now().UTC(),
		ExpiresAt:   time.Now().UTC().Add(SessionDuration),
	}

	data, err := json.Marshal(session)
	if err != nil {
		return err
	}

	encoded := base64.URLEncoding.EncodeToString(data)
	sig := s.sign(encoded)
	val := fmt.Sprintf("%s.%s", encoded, sig)

	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    val,
		Path:     "/",
		HttpOnly: true,
		Secure:   strings.HasPrefix(s.redirectURL, "https://"),
		SameSite: http.SameSiteLaxMode,
		Expires:  session.ExpiresAt,
	})
	return nil
}

// ValidateSession reads, verifies HMAC, and unpacks the session cookie.
func (s *Service) ValidateSession(r *http.Request) (*SessionPayload, error) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		return nil, err
	}

	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 2 {
		return nil, errors.New("malformed session cookie")
	}

	encoded, sig := parts[0], parts[1]
	if !hmac.Equal([]byte(sig), []byte(s.sign(encoded))) {
		return nil, errors.New("invalid session signature")
	}

	data, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}

	var session SessionPayload
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, err
	}

	if time.Now().UTC().After(session.ExpiresAt) {
		return nil, errors.New("session expired")
	}

	return &session, nil
}

// ClearSessionCookie deletes the session cookie upon logout.
func (s *Service) ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	})
}

func (s *Service) sign(data string) string {
	mac := hmac.New(sha256.New, s.sessionSecret)
	mac.Write([]byte(data))
	return base64.URLEncoding.EncodeToString(mac.Sum(nil))
}
