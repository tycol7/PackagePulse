package agent

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/tylerdean/package-tracker-demo/internal/models"
	"github.com/tylerdean/package-tracker-demo/internal/storage"
)

var (
	trackingRegex     = regexp.MustCompile(`(?i)(1Z[0-9A-Z]{16}|\b\d{12,22}\b)`)
	fedexRegex        = regexp.MustCompile(`(?i)\b\d{12}\b`)
	deliveryDateRegex = regexp.MustCompile(`(?i)(?:estimated\s+delivery|delivery\s+date|scheduled\s+delivery):\s*(\d{4}-\d{2}-\d{2})`)
)

// MockAgent provides a local, offline implementation of LogisticsAgent.
type MockAgent struct{}

func NewMockAgent() *MockAgent {
	return &MockAgent{}
}

func (m *MockAgent) Close() error {
	return nil
}

func (m *MockAgent) ProcessEmail(ctx context.Context, email storage.ParsedEmail) (*models.ExtractionResult, error) {
	combined := strings.ToLower(email.Subject + " " + email.Body)

	// Rejection heuristics
	if strings.Contains(combined, "newsletter") ||
		strings.Contains(combined, "weekly digest") ||
		(strings.Contains(combined, "unsubscribe") && !strings.Contains(combined, "tracking") && !strings.Contains(combined, "shipped")) ||
		((strings.Contains(combined, "receipt") || strings.Contains(combined, "invoice") || strings.Contains(combined, "payment") || strings.Contains(combined, "subscription")) &&
			!strings.Contains(combined, "tracking") && !strings.Contains(combined, "shipped") && !strings.Contains(combined, "carrier")) {
		return &models.ExtractionResult{
			IsPackageEmail:  false,
			Confidence:      0.95,
			Reasoning:       "Classified as non-package communication (promotional newsletter or service billing invoice without shipping details).",
			RejectionReason: "Classified as non-package communication",
		}, nil
	}

	// Acceptance heuristics
	sender := "Google Store"
	if strings.Contains(combined, "google store") || strings.Contains(combined, "google") {
		sender = "Google Store"
	} else if strings.Contains(combined, "amazon") {
		sender = "Amazon.com"
	} else if strings.Contains(combined, "b&h") || strings.Contains(combined, "bhphoto") {
		sender = "B&H Photo Video"
	} else if strings.Contains(combined, "apple") {
		sender = "Apple Store"
	} else if strings.Contains(combined, "fedex") {
		sender = "FedEx Shipper"
	}

	carrier := "FedEx"
	if strings.Contains(combined, "ups") {
		carrier = "UPS"
	} else if strings.Contains(combined, "usps") {
		carrier = "USPS"
	} else if strings.Contains(combined, "amazon") {
		carrier = "Amazon Logistics"
	} else if strings.Contains(combined, "dhl") {
		carrier = "DHL"
	}

	// Find all tracking numbers in the email
	var trackings []string
	seenTracking := make(map[string]bool)

	allMatches := trackingRegex.FindAllString(email.Body, -1)
	for _, m := range allMatches {
		if !seenTracking[m] {
			seenTracking[m] = true
			trackings = append(trackings, m)
		}
	}
	if len(trackings) == 0 {
		fedexMatches := fedexRegex.FindAllString(email.Body, -1)
		for _, m := range fedexMatches {
			if !seenTracking[m] {
				seenTracking[m] = true
				trackings = append(trackings, m)
			}
		}
	}

	if len(trackings) == 0 {
		trackings = append(trackings, "773918274619")
	}

	status := "In Transit"
	if strings.Contains(combined, "out for delivery") {
		status = "Out for Delivery"
	} else if strings.Contains(combined, "delivered") {
		status = "Delivered"
	} else if strings.Contains(combined, "order confirmed") || strings.Contains(combined, "ordered") {
		status = "Ordered"
	} else if strings.Contains(combined, "exception") || strings.Contains(combined, "delayed") {
		status = "Exception"
	}

	notes := "Electronics / Hardware items"
	if strings.Contains(combined, "pixel") {
		notes = "Google Pixel Device & Accessories"
	} else if strings.Contains(combined, "macbook") {
		notes = "Apple MacBook"
	}

	var extractedPkgs []*models.ExtractedFields
	for i, tracking := range trackings {
		pkgCarrier := carrier
		if strings.HasPrefix(strings.ToUpper(tracking), "1Z") {
			pkgCarrier = "UPS"
		} else if len(tracking) == 12 && pkgCarrier != "FedEx" {
			pkgCarrier = "FedEx"
		}

		pkgLink := ""
		if pkgCarrier == "FedEx" {
			pkgLink = fmt.Sprintf("https://www.fedex.com/fedextrack/?trknbr=%s", tracking)
		} else if pkgCarrier == "UPS" {
			pkgLink = fmt.Sprintf("https://www.ups.com/track?tracknum=%s", tracking)
		} else if pkgCarrier == "USPS" {
			pkgLink = fmt.Sprintf("https://tools.usps.com/go/TrackConfirmAction?tLabels=%s", tracking)
		}

		pkgNotes := notes
		if len(trackings) > 1 {
			pkgNotes = fmt.Sprintf("%s (Item %d of %d)", notes, i+1, len(trackings))
		}

		expectedDate := storage.DeduceDeliveryDate(combined, email.SentAt)
		if expectedDate == "" {
			if m := deliveryDateRegex.FindStringSubmatch(email.Body); len(m) > 1 {
				expectedDate = m[1]
			}
		}

		extractedPkgs = append(extractedPkgs, &models.ExtractedFields{
			Sender:               sender,
			Carrier:              pkgCarrier,
			TrackingNumber:       tracking,
			TrackingLink:         pkgLink,
			ExpectedDeliveryDate: expectedDate,
			Status:               status,
			Notes:                pkgNotes,
		})
	}

	dateNote := ""
	if email.SentAt != nil && storage.HasTomorrowDelivery(combined) {
		dateNote = fmt.Sprintf(" Inbound email specified tomorrow delivery; deduced expected delivery date as %s based on email sent date (%s).", extractedPkgs[0].ExpectedDeliveryDate, email.SentAt.Format("2006-01-02"))
	} else if email.SentAt == nil && (storage.HasTomorrowDelivery(combined) || storage.HasTodayDelivery(combined)) {
		dateNote = " Inbound email mentions relative delivery timing, but has no sent Date header; delivery date cannot be deduced without guessing."
	}

	reasoning := fmt.Sprintf("Classified as shipping tracking update. Detected %d package shipment(s), carrier '%s', tracking numbers: %s, and status '%s'.%s",
		len(extractedPkgs), carrier, strings.Join(trackings, ", "), status, dateNote)

	return &models.ExtractionResult{
		IsPackageEmail:    true,
		Confidence:        0.98,
		Reasoning:         reasoning,
		ExtractedPackages: extractedPkgs,
		ExtractedPackage:  extractedPkgs[0],
	}, nil
}

func (m *MockAgent) ComposeDigest(ctx context.Context, user *models.User, packages []*models.Package) (string, error) {
	var itemsHTML strings.Builder
	for _, p := range packages {
		link := p.TrackingLink
		if link == "" {
			link = "#"
		}
		itemsHTML.WriteString(fmt.Sprintf(`
		<div style="background: #ffffff; border: 1px solid #dadce0; border-radius: 8px; padding: 16px; margin-bottom: 12px;">
			<div style="display: flex; justify-content: space-between; align-items: center;">
				<h3 style="margin: 0; color: #202124; font-size: 16px;">%s</h3>
				<span style="background: #e8f0fe; color: #1a73e8; padding: 4px 8px; border-radius: 4px; font-size: 12px; font-weight: 500;">%s</span>
			</div>
			<p style="margin: 8px 0 4px; color: #5f6368; font-size: 14px;"><strong>Carrier:</strong> %s &bull; <strong>Tracking:</strong> <a href="%s" style="color: #1a73e8; text-decoration: none;">%s</a></p>
			<p style="margin: 0; color: #3c4043; font-size: 14px;"><strong>Contents:</strong> %s</p>
		</div>`, p.Sender, p.Status, p.Carrier, link, p.TrackingNumber, p.Notes))
	}

	return fmt.Sprintf(`
	<div style="font-family: 'Roboto', -apple-system, BlinkMacSystemFont, 'Segoe UI', Arial, sans-serif; max-width: 600px; margin: 0 auto; background: #f8f9fa; padding: 24px; border-radius: 12px;">
		<div style="border-bottom: 2px solid #1a73e8; padding-bottom: 16px; margin-bottom: 20px;">
			<h2 style="margin: 0; color: #1a73e8; font-size: 22px;">📦 Good morning, %s!</h2>
			<p style="margin: 6px 0 0; color: #5f6368; font-size: 14px;">Here is your personal package arrival briefing for today (%s).</p>
		</div>
		<p style="color: #202124; font-size: 15px; margin-bottom: 16px;">You have <strong>%d package(s)</strong> scheduled to arrive at your doorstep today:</p>
		%s
		<div style="background: #e6f4ea; border-left: 4px solid #34a853; padding: 12px 16px; border-radius: 4px; margin-top: 20px;">
			<p style="margin: 0; color: #137333; font-size: 13px;">💡 <strong>Delivery Tip:</strong> Drivers frequently deliver between 10 AM and 4 PM. Keep an eye out for carrier drop-offs!</p>
		</div>
		<div style="margin-top: 24px; text-align: center; color: #70757a; font-size: 12px;">
			<p>Sent by PackagePulse AI Logistics Agent &bull; Powered by Google Cloud Agent Platform &bull; Gemini</p>
		</div>
	</div>`, user.DisplayName, time.Now().Format("Monday, Jan 2"), len(packages), itemsHTML.String()), nil
}

func (m *MockAgent) Query(ctx context.Context, prompt string) (string, error) {
	return "Hello! I am the PackagePulse Logistics Agent running on Google Cloud Agent Platform. I can track deliveries, parse inbound emails, and generate morning logistics briefings.", nil
}

