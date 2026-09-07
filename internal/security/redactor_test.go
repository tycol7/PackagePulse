package security_test

import (
	"strings"
	"testing"

	"github.com/tylerdean/package-tracker-demo/internal/security"
)

func TestRedactPII(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains []string
		omits    []string
	}{
		{
			name:     "Redacts email addresses",
			input:    "Contact customer at user.smith@example.com or support@store.google.com",
			contains: []string{"[EMAIL_REDACTED]"},
			omits:    []string{"user.smith@example.com", "support@store.google.com"},
		},
		{
			name:     "Redacts US phone numbers",
			input:    "Call delivery driver at 555-234-5678 or +1 (555) 345-6789 if not home",
			contains: []string{"[PHONE_REDACTED]"},
			omits:    []string{"555-234-5678", "555) 345-6789"},
		},
		{
			name:     "Redacts physical street address",
			input:    "Delivering to 742 Evergreen Terrace, Springfield, OR 97477",
			contains: []string{"[ADDRESS_REDACTED]"},
			omits:    []string{"742 Evergreen Terrace"},
		},
		{
			name:     "Redacts SSN",
			input:    "SSN on file: 123-45-6789 for customs verification",
			contains: []string{"[SSN_REDACTED]"},
			omits:    []string{"123-45-6789"},
		},
		{
			name:     "Redacts Credit Cards but preserves tracking numbers",
			input:    "Paid with Visa 4111 2222 3333 4444. Tracking number is 773918274619 via FedEx.",
			contains: []string{"[CREDENTIAL_REDACTED]", "773918274619", "FedEx"},
			omits:    []string{"4111 2222 3333 4444"},
		},
		{
			name:     "Redacts Bearer tokens and API keys",
			input:    "Authorization: Bearer sample_auth_token_secret_string_1234567890",
			contains: []string{"[SECRET_REDACTED]"},
			omits:    []string{"sample_auth_token_secret_string_1234567890"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := security.RedactPII(tt.input)
			for _, c := range tt.contains {
				if !strings.Contains(got, c) {
					t.Errorf("expected redacted output to contain %q, got: %s", c, got)
				}
			}
			for _, o := range tt.omits {
				if strings.Contains(got, o) {
					t.Errorf("expected redacted output to NOT contain %q, got: %s", o, got)
				}
			}
		})
	}
}

func TestSanitizeLogMap(t *testing.T) {
	rawMap := map[string]interface{}{
		"sender":   "John Doe <john.doe@example.com>",
		"address":  "123 Main St, Springfield, IL 62701",
		"tracking": "773918274619",
		"carrier":  "FedEx",
		"meta": map[string]interface{}{
			"driver_phone": "(555) 456-7890",
		},
	}

	clean := security.SanitizeLogMap(rawMap)

	if strings.Contains(clean["sender"].(string), "john.doe@example.com") {
		t.Errorf("expected sender email to be redacted in log map, got: %v", clean["sender"])
	}
	if !strings.Contains(clean["sender"].(string), "[EMAIL_REDACTED]") {
		t.Errorf("expected [EMAIL_REDACTED] in sender, got: %v", clean["sender"])
	}

	if strings.Contains(clean["address"].(string), "123 Main St") {
		t.Errorf("expected address to be redacted in log map, got: %v", clean["address"])
	}

	if clean["tracking"].(string) != "773918274619" {
		t.Errorf("expected tracking number to be preserved, got: %v", clean["tracking"])
	}

	subMeta := clean["meta"].(map[string]interface{})
	if strings.Contains(subMeta["driver_phone"].(string), "555") {
		t.Errorf("expected driver phone to be redacted in nested map, got: %v", subMeta["driver_phone"])
	}
}

func TestRedactEmailPayload(t *testing.T) {
	eml := `From: Shipper Updates <orders@merchant.com>
To: Tyler Dean <tylerdean@google.com>
Subject: Order Confirmation for Tyler Dean
Date: Fri, 04 Sep 2026 10:00:00 -0700

Hello Tyler,
Your package is being delivered to:
456 Oak Street, Seattle, WA 98101
Phone: (206) 555-0199

Tracking Number: 123456789012
Carrier: FedEx
Status: In Transit
Expected Delivery: tomorrow
`

	redacted := string(security.RedactEmailPayload("order.eml", []byte(eml)))

	if strings.Contains(redacted, "tylerdean@google.com") {
		t.Errorf("expected user email to be redacted from email payload")
	}
	if strings.Contains(redacted, "orders@merchant.com") {
		t.Errorf("expected merchant email to be redacted from email payload")
	}
	if strings.Contains(redacted, "456 Oak Street") {
		t.Errorf("expected street address to be redacted from email payload")
	}
	if strings.Contains(redacted, "(206) 555-0199") {
		t.Errorf("expected phone to be redacted from email payload")
	}

	// Must preserve tracking number and logistics signals
	if !strings.Contains(redacted, "123456789012") {
		t.Errorf("expected tracking number to be preserved in redacted payload")
	}
	if !strings.Contains(redacted, "FedEx") {
		t.Errorf("expected carrier to be preserved in redacted payload")
	}
	if !strings.Contains(redacted, "Date: Fri, 04 Sep 2026 10:00:00 -0700") {
		t.Errorf("expected Date header to be preserved in redacted payload")
	}
}
