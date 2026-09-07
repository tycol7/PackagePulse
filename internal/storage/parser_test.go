package storage_test

import (
	"os"
	"strings"
	"testing"

	"github.com/tylerdean/package-tracker-demo/internal/storage"
)

func TestExtractTextFromPayload_EML(t *testing.T) {
	data, err := os.ReadFile("../../static/samples/fedex_in_transit.eml")
	if err != nil {
		t.Fatalf("failed reading sample eml: %v", err)
	}

	parsed := storage.ExtractTextFromPayload("fedex_in_transit.eml", data)

	if !strings.Contains(parsed.Subject, "773918274619") {
		t.Errorf("expected subject to contain tracking number, got: %s", parsed.Subject)
	}
	if !strings.Contains(parsed.Body, "Pixel 9 Pro") {
		t.Errorf("expected body to mention Pixel 9 Pro, got: %s", parsed.Body)
	}
}

func TestExtractTextFromPayload_TXT(t *testing.T) {
	data, err := os.ReadFile("../../static/samples/fedex_out_for_delivery.txt")
	if err != nil {
		t.Fatalf("failed reading sample txt: %v", err)
	}

	parsed := storage.ExtractTextFromPayload("fedex_out_for_delivery.txt", data)

	if !strings.Contains(parsed.Subject, "OUT FOR DELIVERY") {
		t.Errorf("expected subject to contain OUT FOR DELIVERY, got: %s", parsed.Subject)
	}
	if !strings.Contains(parsed.Body, "773918274619") {
		t.Errorf("expected body to contain tracking number, got: %s", parsed.Body)
	}
}

func TestExtractTextFromPayload_Newsletter(t *testing.T) {
	data, err := os.ReadFile("../../static/samples/marketing_newsletter.eml")
	if err != nil {
		t.Fatalf("failed reading sample newsletter: %v", err)
	}

	parsed := storage.ExtractTextFromPayload("marketing_newsletter.eml", data)

	if !strings.Contains(parsed.Subject, "Cloud Tech Weekly") {
		t.Errorf("expected subject to match newsletter, got: %s", parsed.Subject)
	}
	if !strings.Contains(parsed.Body, "unsubscribe") {
		t.Errorf("expected body to contain unsubscribe, got: %s", parsed.Body)
	}
}

func TestExtractTextFromPayload_DateAndTomorrowDeduction(t *testing.T) {
	// Sample EML with sent date Sep 4, 2026 stating delivery is tomorrow
	emlContent := "From: shipper@example.com\n" +
		"Subject: Shipment Update: Arriving Tomorrow!\n" +
		"Date: Fri, 04 Sep 2026 15:30:00 -0700\n" +
		"Content-Type: text/plain\n\n" +
		"Your package with tracking number 773918274619 will be delivered tomorrow by FedEx.\n"

	parsed := storage.ExtractTextFromPayload("update.eml", []byte(emlContent))

	if parsed.Date == "" {
		t.Errorf("expected parsed.Date to be non-empty")
	}
	if parsed.SentAt == nil {
		t.Fatalf("expected parsed.SentAt to be parsed, got nil")
	}
	if parsed.SentAt.Format("2006-01-02") != "2026-09-04" {
		t.Errorf("expected SentAt date 2026-09-04, got: %s", parsed.SentAt.Format("2006-01-02"))
	}

	if !storage.HasTomorrowDelivery(parsed.Body) {
		t.Errorf("expected HasTomorrowDelivery to return true")
	}

	deducedDate := storage.DeduceDeliveryDate(parsed.Body, parsed.SentAt)
	if deducedDate != "2026-09-05" {
		t.Errorf("expected deduced delivery date to be 2026-09-05 (sent 2026-09-04 + 1 day), got: %s", deducedDate)
	}

	// Sample TXT with date header
	txtContent := "From: shipper@example.com\n" +
		"Subject: Scheduled delivery for tomorrow\n" +
		"Date: 2026-10-15\n\n" +
		"Your order will be delivered tomorrow.\n"

	parsedTXT := storage.ExtractTextFromPayload("update.txt", []byte(txtContent))
	if parsedTXT.SentAt == nil {
		t.Fatalf("expected parsedTXT.SentAt to be parsed, got nil")
	}
	deducedTXT := storage.DeduceDeliveryDate(parsedTXT.Body, parsedTXT.SentAt)
	if deducedTXT != "2026-10-16" {
		t.Errorf("expected deduced delivery date 2026-10-16, got: %s", deducedTXT)
	}
}

func TestExtractTextFromPayload_MissingDateHeader_NoGuessing(t *testing.T) {
	// Inbound email with "tomorrow" but NO Date header
	content := "From: shipper@example.com\n" +
		"Subject: Scheduled delivery for tomorrow\n\n" +
		"Your package 773918274619 will arrive tomorrow.\n"

	parsed := storage.ExtractTextFromPayload("update_nodate.eml", []byte(content))
	if parsed.SentAt != nil {
		t.Errorf("expected SentAt to be nil when Date header is missing, got: %v", parsed.SentAt)
	}
	if parsed.DateString() != "Not specified" {
		t.Errorf("expected DateString() to be 'Not specified', got: %q", parsed.DateString())
	}

	// DeduceDeliveryDate MUST return "" and NOT guess or default to current time
	deduced := storage.DeduceDeliveryDate(parsed.Body, parsed.SentAt)
	if deduced != "" {
		t.Errorf("expected empty deduced delivery date when SentAt is nil, got: %q", deduced)
	}
}

