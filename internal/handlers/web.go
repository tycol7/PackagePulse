package handlers

import (
	"html/template"
	"log"
	"net/http"
	"strings"

	"github.com/tylerdean/package-tracker-demo/internal/auth"
	"github.com/tylerdean/package-tracker-demo/internal/db"
	"github.com/tylerdean/package-tracker-demo/internal/models"
)

type WebHandler struct {
	tmpl        *template.Template
	store       db.Store
	authService *auth.Service
}

func NewWebHandler(tmpl *template.Template, store db.Store, authService *auth.Service) *WebHandler {
	return &WebHandler{
		tmpl:        tmpl,
		store:       store,
		authService: authService,
	}
}

// ShowLogin renders the Google Sign-In page.
func (h *WebHandler) ShowLogin(w http.ResponseWriter, r *http.Request) {
	// If already logged in, redirect to dashboard
	if session, err := h.authService.ValidateSession(r); err == nil && session != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	data := map[string]interface{}{
		"Title": "Sign In | PackagePulse",
	}
	if err := h.tmpl.ExecuteTemplate(w, "login.html", data); err != nil {
		log.Printf("Error rendering login: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// StartGoogleOAuth initiates Google OAuth redirect.
func (h *WebHandler) StartGoogleOAuth(w http.ResponseWriter, r *http.Request) {
	url, err := h.authService.GetLoginURL(w)
	if err != nil {
		http.Error(w, "Failed to generate login URL", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, url, http.StatusTemporaryRedirect)
}

// HandleGoogleCallback processes the OAuth code from accounts.google.com.
func (h *WebHandler) HandleGoogleCallback(w http.ResponseWriter, r *http.Request) {
	user, err := h.authService.ExchangeAndValidate(r.Context(), r)
	if err != nil {
		log.Printf("OAuth authentication failed: %v", err)
		http.Error(w, "Authentication Failed: "+err.Error(), http.StatusUnauthorized)
		return
	}

	// Persist user in Firestore if not already present
	existing, err := h.store.GetUser(r.Context(), user.ID)
	if err != nil || existing == nil {
		if err := h.store.SaveUser(r.Context(), user); err != nil {
			log.Printf("Failed saving user profile: %v", err)
		}
	} else {
		user.Preferences = existing.Preferences
	}

	if err := h.authService.IssueSessionCookie(w, user); err != nil {
		http.Error(w, "Failed to create session", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// HandleLogout clears the session cookie.
func (h *WebHandler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	h.authService.ClearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// ShowDashboard renders the primary package dashboard.
func (h *WebHandler) ShowDashboard(w http.ResponseWriter, r *http.Request) {
	session := auth.GetUserFromContext(r.Context())
	if session == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	user, err := h.store.GetUser(r.Context(), session.UserID)
	if err != nil || user == nil {
		// Fallback user from session
		user = &models.User{
			ID:          session.UserID,
			Email:       session.Email,
			DisplayName: session.DisplayName,
			AvatarURL:   session.AvatarURL,
			NominalForwardingAddress: "track+" + session.UserID + "@packagepulse.google.com",
			Preferences: models.UserPreferences{
				DailyDigestEnabled: true,
				DigestTimeLocal:    "08:00",
				Timezone:           "America/Los_Angeles",
				RecipientEmails:    []string{session.Email},
			},
		}
	}

	packages, err := h.store.ListPackages(r.Context(), session.UserID, "")
	if err != nil {
		packages = []*models.Package{}
	}

	counts := models.CalculateStatusCounts(packages)
	data := map[string]interface{}{
		"User":                user,
		"Packages":            packages,
		"TotalCount":          counts.TotalCount,
		"CountInTransit":      counts.CountInTransit,
		"CountOutForDelivery": counts.CountOutForDelivery,
		"CountOrdered":        counts.CountOrdered,
		"CountDelivered":      counts.CountDelivered,
		"ActiveFilter":        "All",
	}

	if err := h.tmpl.ExecuteTemplate(w, "dashboard.html", data); err != nil {
		log.Printf("Error rendering dashboard: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// SaveSettings updates the user's notification preferences.
func (h *WebHandler) SaveSettings(w http.ResponseWriter, r *http.Request) {
	session := auth.GetUserFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	user, err := h.store.GetUser(r.Context(), session.UserID)
	if err != nil || user == nil {
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}

	user.Preferences.DailyDigestEnabled = r.FormValue("daily_digest_enabled") == "on"
	user.Preferences.DigestTimeLocal = r.FormValue("digest_time_local")
	user.Preferences.Timezone = r.FormValue("timezone")

	rawEmails := r.FormValue("recipient_emails")
	var emails []string
	for _, e := range strings.Split(rawEmails, ",") {
		clean := strings.TrimSpace(e)
		if clean != "" {
			emails = append(emails, clean)
		}
	}
	user.Preferences.RecipientEmails = emails

	if err := h.store.SaveUser(r.Context(), user); err != nil {
		http.Error(w, "Failed saving preferences", http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Trigger", `{"showToast": "Settings saved successfully!"}`)
	w.WriteHeader(http.StatusOK)
}
