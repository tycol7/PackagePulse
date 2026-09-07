package templates_test

import (
	"bytes"
	"html/template"
	"strings"
	"testing"

	"github.com/tylerdean/package-tracker-demo/internal/models"
	"github.com/tylerdean/package-tracker-demo/internal/templates"
)

func TestDashboardTemplate(t *testing.T) {
	tmpl, err := template.ParseFS(templates.FS, "*.html")
	if err != nil {
		t.Fatalf("Failed to parse templates: %v", err)
	}

	data := map[string]interface{}{
		"User": &models.User{
			DisplayName: "Tyler Dean",
			Email:       "tylerdean@google.com",
		},
		"Packages":            []*models.Package{},
		"TotalCount":          0,
		"CountInTransit":      0,
		"CountOutForDelivery": 0,
		"CountOrdered":        0,
		"CountDelivered":      0,
		"ActiveFilter":        "All",
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "dashboard.html", data); err != nil {
		t.Fatalf("Failed to execute dashboard.html: %v", err)
	}

	output := buf.String()
	if strings.Contains(output, "Sign in with Google") {
		t.Errorf("BUG: dashboard.html rendered the login screen! Content block collision in Go html/template")
	}
	if !strings.Contains(output, "Inbound Email Agent Ingestion") {
		t.Errorf("Expected dashboard to contain 'Inbound Email Agent Ingestion'")
	}
}

func TestPackageCardTemplate(t *testing.T) {
	tmpl, err := template.ParseFS(templates.FS, "*.html")
	if err != nil {
		t.Fatalf("Failed to parse templates: %v", err)
	}

	// 1. Package with literal "null" as TrackingLink
	pkgWithNullLink := &models.Package{
		ID:                   "pkg_test_1",
		Sender:               "Google Store",
		Carrier:              "FedEx",
		TrackingNumber:       "773918274619",
		TrackingLink:         "null",
		ExpectedDeliveryDate: "2026-09-06",
		Status:               models.StatusInTransit,
		Source:               "EMAIL_UPLOAD",
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "package_card", pkgWithNullLink); err != nil {
		t.Fatalf("Failed to execute package_card: %v", err)
	}

	cardOutput := buf.String()

	// Verify "Next Status" button is removed
	if strings.Contains(cardOutput, "Next Status") {
		t.Errorf("expected 'Next Status' button to be removed from package card")
	}

	// Verify no <a href="null">
	if strings.Contains(cardOutput, `href="null"`) {
		t.Errorf("expected no href=\"null\" in rendered package card")
	}

	// 2. Package with valid TrackingLink
	pkgWithValidLink := &models.Package{
		ID:                   "pkg_test_2",
		Sender:               "Amazon",
		Carrier:              "UPS",
		TrackingNumber:       "1Z9999999999999999",
		TrackingLink:         "https://www.ups.com/track?tracknum=1Z9999999999999999",
		ExpectedDeliveryDate: "2026-09-06",
		Status:               models.StatusInTransit,
		Source:               "EMAIL_UPLOAD",
	}

	var buf2 bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf2, "package_card", pkgWithValidLink); err != nil {
		t.Fatalf("Failed to execute package_card: %v", err)
	}

	cardOutput2 := buf2.String()
	if !strings.Contains(cardOutput2, `href="https://www.ups.com/track?tracknum=1Z9999999999999999"`) {
		t.Errorf("expected valid tracking URL to be rendered")
	}
}
