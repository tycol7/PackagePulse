package models

import (
	"strings"
	"time"
)

// PackageStatus represents the lifecycle state of a shipment.
type PackageStatus string

const (
	StatusOrdered        PackageStatus = "Ordered"
	StatusInTransit      PackageStatus = "In Transit"
	StatusOutForDelivery PackageStatus = "Out for Delivery"
	StatusDelivered      PackageStatus = "Delivered"
	StatusException      PackageStatus = "Exception"
)

// Package represents a tracked parcel.
type Package struct {
	ID                   string        `json:"id" firestore:"id"`
	UserID               string        `json:"user_id" firestore:"user_id"`
	Sender               string        `json:"sender" firestore:"sender"`
	Carrier              string        `json:"carrier" firestore:"carrier"`
	TrackingNumber       string        `json:"tracking_number" firestore:"tracking_number"`
	TrackingLink         string        `json:"tracking_link" firestore:"tracking_link"`
	ExpectedDeliveryDate string        `json:"expected_delivery_date" firestore:"expected_delivery_date"` // YYYY-MM-DD
	Status               PackageStatus `json:"status" firestore:"status"`
	Notes                string        `json:"notes" firestore:"notes"`
	Source               string        `json:"source" firestore:"source"` // "MANUAL", "EMAIL_UPLOAD"
	RawEmailSubject      string        `json:"raw_email_subject,omitempty" firestore:"raw_email_subject,omitempty"`
	CreatedAt            time.Time     `json:"created_at" firestore:"created_at"`
	UpdatedAt            time.Time     `json:"updated_at" firestore:"updated_at"`
}

// UserPreferences holds notification settings for a user.
type UserPreferences struct {
	DailyDigestEnabled bool       `json:"daily_digest_enabled" firestore:"daily_digest_enabled"`
	DigestTimeLocal    string     `json:"digest_time_local" firestore:"digest_time_local"` // e.g. "08:00"
	Timezone           string     `json:"timezone" firestore:"timezone"`                   // e.g. "America/Los_Angeles"
	RecipientEmails    []string   `json:"recipient_emails" firestore:"recipient_emails"`   // List of recipient emails
	LastDigestSentAt   *time.Time `json:"last_digest_sent_at,omitempty" firestore:"last_digest_sent_at,omitempty"`
}

// User represents an authenticated Google user.
type User struct {
	ID                       string          `json:"id" firestore:"id"`                     // Google Subject ID (sub)
	Email                    string          `json:"email" firestore:"email"`               // Verified @google.com email
	DisplayName              string          `json:"display_name" firestore:"display_name"` // Full name from Google
	AvatarURL                string          `json:"avatar_url" firestore:"avatar_url"`     // Profile picture URL
	NominalForwardingAddress string          `json:"nominal_forwarding_address" firestore:"nominal_forwarding_address"`
	Preferences              UserPreferences `json:"preferences" firestore:"preferences"`
	CreatedAt                time.Time       `json:"created_at" firestore:"created_at"`
	UpdatedAt                time.Time       `json:"updated_at" firestore:"updated_at"`
}

// ExtractionResult represents the structured extraction output from Gemini 2.5 Flash.
type ExtractionResult struct {
	IsPackageEmail    bool               `json:"is_package_email"`
	Confidence        float64            `json:"confidence"`
	Reasoning         string             `json:"reasoning,omitempty"`
	RejectionReason   string             `json:"rejection_reason,omitempty"`
	ExtractedPackages []*ExtractedFields `json:"packages,omitempty"`
	ExtractedPackage  *ExtractedFields   `json:"package,omitempty"` // Retained for backwards compatibility
}

// AllPackages returns all extracted packages, prioritizing ExtractedPackages slice,
// falling back to ExtractedPackage if only a single package was returned.
// Automatically sanitizes fields (e.g. stripping literal "null" values).
func (r *ExtractionResult) AllPackages() []*ExtractedFields {
	if r == nil {
		return nil
	}
	var res []*ExtractedFields
	if len(r.ExtractedPackages) > 0 {
		for _, p := range r.ExtractedPackages {
			if p != nil {
				p.Sanitize()
				res = append(res, p)
			}
		}
		if r.ExtractedPackage == nil && len(res) > 0 {
			r.ExtractedPackage = res[0]
		}
	} else if r.ExtractedPackage != nil {
		r.ExtractedPackage.Sanitize()
		res = append(res, r.ExtractedPackage)
	}
	return res
}

// ExtractedFields represents the fields extracted from a shipping email.
type ExtractedFields struct {
	Sender               string `json:"sender"`
	Carrier              string `json:"carrier"`
	TrackingNumber       string `json:"tracking_number"`
	TrackingLink         string `json:"tracking_link"`
	ExpectedDeliveryDate string `json:"expected_delivery_date"`
	Status               string `json:"status"`
	Notes                string `json:"notes"`
}

// Sanitize removes literal "null", "none", "nil", "n/a" strings and validates URLs.
func (f *ExtractedFields) Sanitize() {
	if f == nil {
		return
	}
	f.Sender = SanitizeCleanString(f.Sender)
	f.Carrier = SanitizeCleanString(f.Carrier)
	f.TrackingNumber = SanitizeCleanString(f.TrackingNumber)
	f.TrackingLink = SanitizeCleanURL(f.TrackingLink)
	f.ExpectedDeliveryDate = SanitizeCleanString(f.ExpectedDeliveryDate)
	f.Status = SanitizeCleanString(f.Status)
	f.Notes = SanitizeCleanString(f.Notes)
}

// SanitizeCleanString strips leading/trailing spaces and converts literal "null", "nil", "none", etc. to empty string.
func SanitizeCleanString(s string) string {
	trimmed := strings.TrimSpace(s)
	lower := strings.ToLower(trimmed)
	if lower == "null" || lower == "nil" || lower == "none" || lower == "n/a" || lower == "undefined" || lower == "<nil>" {
		return ""
	}
	return trimmed
}

// SanitizeCleanURL validates that the string is a valid http(s) URL and not "null".
func SanitizeCleanURL(s string) string {
	cleaned := SanitizeCleanString(s)
	if cleaned == "" {
		return ""
	}
	lower := strings.ToLower(cleaned)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return ""
	}
	return cleaned
}

// StatusCounts holds aggregate counters for packages.
type StatusCounts struct {
	TotalCount          int
	CountInTransit      int
	CountOutForDelivery int
	CountOrdered        int
	CountDelivered      int
}

// CalculateStatusCounts computes counts across a slice of packages.
func CalculateStatusCounts(packages []*Package) StatusCounts {
	var counts StatusCounts
	counts.TotalCount = len(packages)
	for _, p := range packages {
		switch p.Status {
		case StatusInTransit:
			counts.CountInTransit++
		case StatusOutForDelivery:
			counts.CountOutForDelivery++
		case StatusOrdered:
			counts.CountOrdered++
		case StatusDelivered:
			counts.CountDelivered++
		}
	}
	return counts
}
