package auth

import (
	"context"
	"net/http"
)

type contextKey string

const (
	UserContextKey contextKey = "authenticated_user"
)

// Middleware validates session cookies and populates context with user information.
type Middleware struct {
	authService *Service
}

func NewMiddleware(authService *Service) *Middleware {
	return &Middleware{authService: authService}
}

// RequireAuth ensures the request has a valid session; redirects to /login if unauthenticated.
func (m *Middleware) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session, err := m.authService.ValidateSession(r)
		if err != nil || session == nil {
			// Check if HTMX request, redirect via HX-Redirect
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Redirect", "/login")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		ctx := context.WithValue(r.Context(), UserContextKey, session)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// GetUserFromContext retrieves authenticated session info from context.
func GetUserFromContext(ctx context.Context) *SessionPayload {
	if val, ok := ctx.Value(UserContextKey).(*SessionPayload); ok {
		return val
	}
	return nil
}
