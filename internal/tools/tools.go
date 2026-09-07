package tools

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"cloud.google.com/go/vertexai/genai"
	"github.com/tylerdean/package-tracker-demo/internal/db"
	"github.com/tylerdean/package-tracker-demo/internal/security"
	"github.com/tylerdean/package-tracker-demo/internal/storage"
)

// GuidedErrorRecovery provides structured guidance sent back to the LLM when tool execution or parameter validation fails.
type GuidedErrorRecovery struct {
	Success             bool     `json:"success"`
	ErrorCode           string   `json:"error_code"`
	ErrorMessage        string   `json:"error_message"`
	RecoveryGuidance    string   `json:"recovery_guidance"`
	SuggestedNextAction string   `json:"suggested_next_action"`
	ValidExamples       []string `json:"valid_examples,omitempty"`
}

// ToolSuccessResult represents a successful tool execution with helpful continuation guidance.
type ToolSuccessResult struct {
	Success              bool                   `json:"success"`
	Data                 map[string]interface{} `json:"data"`
	ContinuationGuidance string                 `json:"continuation_guidance"`
}

// ToolExecutionResult captures the unified result of a tool execution.
type ToolExecutionResult struct {
	ToolName string               `json:"tool_name"`
	Success  bool                 `json:"success"`
	Data     map[string]any       `json:"data,omitempty"`
	Error    *GuidedErrorRecovery `json:"error,omitempty"`
	Guidance string               `json:"guidance"`
}

// ResponseMap converts the result into the map structure required by genai.FunctionResponse.
func (r *ToolExecutionResult) ResponseMap() map[string]any {
	m := make(map[string]any)
	m["tool_name"] = r.ToolName
	m["success"] = r.Success
	if r.Success {
		m["data"] = r.Data
		m["guidance"] = r.Guidance
	} else if r.Error != nil {
		m["error"] = map[string]any{
			"error_code":            r.Error.ErrorCode,
			"error_message":         r.Error.ErrorMessage,
			"recovery_guidance":     r.Error.RecoveryGuidance,
			"suggested_next_action": r.Error.SuggestedNextAction,
			"valid_examples":        r.Error.ValidExamples,
		}
		m["guidance"] = r.Error.RecoveryGuidance
	}
	return m
}

var (
	fedex12Regex = regexp.MustCompile(`^\d{12}$`)
	fedex15Regex = regexp.MustCompile(`^\d{15}$`)
	upsRegex     = regexp.MustCompile(`(?i)^1Z[0-9A-Z]{16}$`)
	uspsRegex    = regexp.MustCompile(`^\d{20,22}$`)
)

// GetFunctionDeclarations returns comprehensive, descriptive tool function declarations for Gemini and Agent Platform.
func GetFunctionDeclarations() []*genai.FunctionDeclaration {
	return []*genai.FunctionDeclaration{
		{
			Name: "validate_and_track_carrier_package",
			Description: "Validates carrier identification (FedEx, UPS, USPS, DHL, Amazon Logistics) and tracking number syntax using checksum and carrier pattern matching algorithms. " +
				"Generates canonical HTTPS carrier tracking deep-links. When tracking numbers are malformed or invalid, returns structured guided error recovery instructions to assist the model in correcting extraction.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"carrier": {
						Type: genai.TypeString,
						Description: "The shipping carrier name (e.g., 'FedEx', 'UPS', 'USPS', 'DHL', 'Amazon Logistics', 'Other'). " +
							"Case-insensitive. If unsure, specify 'Other' to trigger heuristic carrier autodetection.",
					},
					"tracking_number": {
						Type: genai.TypeString,
						Description: "The raw tracking number extracted from the email body. " +
							"FedEx must be 12 numeric digits (or 15 for Ground); UPS must start with '1Z' followed by 16 alphanumeric characters; " +
							"USPS must be 20 to 22 numeric digits. Do not pass 'null', 'None', or 'N/A'. If untracked, pass an empty string.",
					},
					"merchant_name": {
						Type: genai.TypeString,
						Description: "Optional name of the sender merchant or shipper (e.g. 'Google Store', 'Amazon', 'Apple') used to disambiguate custom carrier tracking links.",
					},
				},
				Required: []string{"carrier", "tracking_number"},
			},
		},
		{
			Name: "calculate_relative_delivery_date",
			Description: "Deterministically converts relative delivery timeframes (e.g., 'tomorrow', 'today', 'next business day', 'in 2 days') into strict ISO-8601 calendar dates (YYYY-MM-DD) " +
				"anchored strictly to the email's explicit Sent Date header. If the Sent Date header is missing or unparseable, this tool intentionally rejects the calculation with guided error recovery instructions forbidding guessing or using current server time.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"relative_phrase": {
						Type: genai.TypeString,
						Description: "The relative date expression extracted from the shipping notification (e.g., 'delivering tomorrow', 'arriving today', 'out for delivery tomorrow', 'delivered next business day').",
					},
					"email_sent_date": {
						Type: genai.TypeString,
						Description: "The RFC3339, RFC1123, or YYYY-MM-DD formatted timestamp representing when the email was sent (e.g., '2026-09-04T10:00:00-07:00' or '2026-09-04'). " +
							"If no Sent Date header exists in the email, pass an empty string \"\" so the tool can guide you to emit an empty delivery date.",
					},
					"target_timezone": {
						Type: genai.TypeString,
						Description: "Optional IANA timezone identifier (e.g. 'America/Los_Angeles', 'America/New_York') for localized calendar day boundary calculation. Defaults to UTC.",
					},
				},
				Required: []string{"relative_phrase", "email_sent_date"},
			},
		},
		{
			Name: "lookup_existing_shipment",
			Description: "Queries the user's active logistics manifest in Cloud Firestore to find existing package records matching the tracking number or merchant name. " +
				"Enables the agent to perform state reconciliation and determine whether an incoming email represents an incremental status update to an existing package or a brand-new shipment.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"user_id": {
						Type:        genai.TypeString,
						Description: "The unique identifier of the authenticated user whose package manifest is being queried.",
					},
					"tracking_number": {
						Type:        genai.TypeString,
						Description: "The carrier tracking number to search for within the user's active packages.",
					},
					"carrier": {
						Type:        genai.TypeString,
						Description: "Optional carrier name to filter by, narrowing down multi-carrier search conflicts.",
					},
				},
				Required: []string{"user_id", "tracking_number"},
			},
		},
		{
			Name: "reconcile_package_status_transition",
			Description: "Validates and governs the lifecycle progression of a package (Ordered -> In Transit -> Out for Delivery -> Delivered). " +
				"Enforces state transition invariants, preventing illegal regressions (such as moving from 'Delivered' back to 'Ordered') unless an explicit carrier shipping exception or return is documented, and returns guided error recovery instructions when conflicts occur.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"current_status": {
						Type: genai.TypeString,
						Description: "The existing package status in the database ('Ordered', 'In Transit', 'Out for Delivery', 'Delivered', 'Exception', or 'NONE' for new packages).",
					},
					"new_status": {
						Type: genai.TypeString,
						Description: "The proposed new status extracted from the inbound notification ('Ordered', 'In Transit', 'Out for Delivery', 'Delivered', 'Exception').",
					},
					"has_exception": {
						Type: genai.TypeBoolean,
						Description: "Set to true if the shipping email explicitly mentions delivery exceptions, weather delays, incorrect address, or package return to sender.",
					},
					"carrier_notes": {
						Type: genai.TypeString,
						Description: "Supporting excerpt from the email explaining the status transition context.",
					},
				},
				Required: []string{"current_status", "new_status"},
			},
		},
		{
			Name: "sanitize_and_extract_item_notes",
			Description: "Sanitizes, normalizes, and extracts purchased item descriptions or package contents. " +
				"Strips tracking links, PII (names, phone numbers, addresses, credit cards), and authentication tokens while producing a clean, human-readable summary of items suitable for display on package cards and morning executive briefings.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"raw_item_text": {
						Type: genai.TypeString,
						Description: "Unfiltered excerpt describing items or order summary from the email body.",
					},
					"max_length": {
						Type: genai.TypeInteger,
						Description: "Maximum allowed character length for the synthesized summary note (default 120 chars).",
					},
				},
				Required: []string{"raw_item_text"},
			},
		},
	}
}

// GetAgentPlatformTool wraps the function declarations into a Google Cloud Agent Platform genai.Tool.
func GetAgentPlatformTool() *genai.Tool {
	return &genai.Tool{
		FunctionDeclarations: GetFunctionDeclarations(),
	}
}

// ExecuteValidateAndTrackCarrierPackage validates carrier and tracking number, generating canonical tracking link.
func ExecuteValidateAndTrackCarrierPackage(carrier, trackingNumber, merchantName string) *ToolExecutionResult {
	res := &ToolExecutionResult{
		ToolName: "validate_and_track_carrier_package",
	}

	cleanTracking := strings.TrimSpace(trackingNumber)
	if cleanTracking == "" || strings.EqualFold(cleanTracking, "null") || strings.EqualFold(cleanTracking, "none") || strings.EqualFold(cleanTracking, "n/a") {
		res.Success = false
		res.Error = &GuidedErrorRecovery{
			Success:             false,
			ErrorCode:           "EMPTY_TRACKING_NUMBER",
			ErrorMessage:        "The provided tracking_number was empty or contained only whitespace/null.",
			RecoveryGuidance:    "Inspect the email body for shipping tracking codes. For FedEx look for 12 numeric digits; for UPS look for '1Z' followed by 16 alphanumeric characters; for USPS look for 20-22 numeric digits. If no tracking number exists in the email, set tracking_number to an empty string \"\" and mark status as 'Ordered'.",
			SuggestedNextAction: "EXTRACT_VALID_TRACKING_OR_OMIT",
			ValidExamples: []string{
				"773918274619 (FedEx 12 digits)",
				"1Z9999999999999999 (UPS 18 chars starting with 1Z)",
				"9400111899562537628832 (USPS 22 digits)",
			},
		}
		res.Guidance = res.Error.RecoveryGuidance
		return res
	}

	normCarrier := strings.TrimSpace(carrier)
	cLower := strings.ToLower(normCarrier)

	// Autodetect carrier if requested or generic
	if cLower == "other" || cLower == "" {
		if upsRegex.MatchString(cleanTracking) {
			normCarrier = "UPS"
			cLower = "ups"
		} else if fedex12Regex.MatchString(cleanTracking) || fedex15Regex.MatchString(cleanTracking) {
			normCarrier = "FedEx"
			cLower = "fedex"
		} else if uspsRegex.MatchString(cleanTracking) {
			normCarrier = "USPS"
			cLower = "usps"
		}
	}

	var trackingLink string
	switch {
	case strings.Contains(cLower, "fedex"):
		normCarrier = "FedEx"
		if !fedex12Regex.MatchString(cleanTracking) && !fedex15Regex.MatchString(cleanTracking) {
			res.Success = false
			res.Error = &GuidedErrorRecovery{
				Success:             false,
				ErrorCode:           "INVALID_FEDEX_TRACKING_LENGTH",
				ErrorMessage:        fmt.Sprintf("FedEx tracking numbers must consist of 12 numeric digits (Express/Standard) or 15 numeric digits (Ground). Received '%s' (%d digits).", cleanTracking, len(cleanTracking)),
				RecoveryGuidance:    "Re-scan the email body specifically for a 12-digit number sequence (regex '\\b\\d{12}\\b'). If the carrier was actually UPS (starting with 1Z) or USPS (20-22 digits), switch the carrier parameter accordingly.",
				SuggestedNextAction: "RETRY_WITH_CORRECTED_FEDEX_TRACKING",
				ValidExamples:       []string{"773918274619", "773918274620"},
			}
			res.Guidance = res.Error.RecoveryGuidance
			return res
		}
		trackingLink = fmt.Sprintf("https://www.fedex.com/fedextrack/?trknbr=%s", cleanTracking)

	case strings.Contains(cLower, "ups"):
		normCarrier = "UPS"
		if !upsRegex.MatchString(cleanTracking) {
			res.Success = false
			res.Error = &GuidedErrorRecovery{
				Success:             false,
				ErrorCode:           "INVALID_UPS_TRACKING_FORMAT",
				ErrorMessage:        fmt.Sprintf("UPS tracking numbers must begin with '1Z' and contain 18 total alphanumeric characters. Received '%s'.", cleanTracking),
				RecoveryGuidance:    "Re-scan the email text for the 18-character '1Z' pattern. If the number is purely numeric, re-evaluate if the carrier is actually FedEx (12 digits) or USPS (20-22 digits).",
				SuggestedNextAction: "RETRY_WITH_CORRECTED_UPS_TRACKING",
				ValidExamples:       []string{"1Z9999999999999999", "1Z12345E0205271688"},
			}
			res.Guidance = res.Error.RecoveryGuidance
			return res
		}
		trackingLink = fmt.Sprintf("https://www.ups.com/track?tracknum=%s", cleanTracking)

	case strings.Contains(cLower, "usps"):
		normCarrier = "USPS"
		if !uspsRegex.MatchString(cleanTracking) {
			res.Success = false
			res.Error = &GuidedErrorRecovery{
				Success:             false,
				ErrorCode:           "INVALID_USPS_TRACKING_LENGTH",
				ErrorMessage:        fmt.Sprintf("USPS package tracking numbers must contain 20 to 22 numeric digits. Received '%s' (%d characters).", cleanTracking, len(cleanTracking)),
				RecoveryGuidance:    "Check if the postal tracking number includes spaces or hyphens that need stripping, or if it is a 22-digit barcode sequence (e.g. starting with 92, 93, or 94).",
				SuggestedNextAction: "RETRY_WITH_CORRECTED_USPS_TRACKING",
				ValidExamples:       []string{"9400111899562537628832"},
			}
			res.Guidance = res.Error.RecoveryGuidance
			return res
		}
		trackingLink = fmt.Sprintf("https://tools.usps.com/go/TrackConfirmAction?tLabels=%s", cleanTracking)

	case strings.Contains(cLower, "dhl"):
		normCarrier = "DHL"
		trackingLink = fmt.Sprintf("https://www.dhl.com/en/express/tracking.html?AWB=%s", cleanTracking)

	default:
		normCarrier = "Other"
		trackingLink = ""
	}

	res.Success = true
	res.Data = map[string]any{
		"carrier":         normCarrier,
		"tracking_number": cleanTracking,
		"tracking_link":   trackingLink,
		"is_valid":        true,
	}
	res.Guidance = fmt.Sprintf("Carrier tracking verified for %s (%s). Use this canonical tracking_link in the package manifest.", normCarrier, cleanTracking)
	return res
}

// ExecuteCalculateRelativeDeliveryDate computes exact calendar date from relative phrases without guessing.
func ExecuteCalculateRelativeDeliveryDate(relativePhrase, emailSentDate, targetTimezone string) *ToolExecutionResult {
	res := &ToolExecutionResult{
		ToolName: "calculate_relative_delivery_date",
	}

	cleanSentDate := strings.TrimSpace(emailSentDate)
	if cleanSentDate == "" || strings.EqualFold(cleanSentDate, "null") || strings.EqualFold(cleanSentDate, "not specified") {
		res.Success = false
		res.Error = &GuidedErrorRecovery{
			Success:             false,
			ErrorCode:           "MISSING_REFERENCE_SENT_DATE",
			ErrorMessage:        "Email Sent Date header is missing or empty. Cannot calculate relative delivery date without a calendar anchor.",
			RecoveryGuidance:    "STRICT SAFETY RULE: You MUST NOT guess, extrapolate, or use current server time when the email lacks an explicit Sent Date header. You MUST leave expected_delivery_date as an empty string (\"\") and note in the reasoning that relative delivery timing lacks a reference date header.",
			SuggestedNextAction: "EMIT_EMPTY_DELIVERY_DATE",
			ValidExamples:       []string{"expected_delivery_date: \"\""},
		}
		res.Guidance = res.Error.RecoveryGuidance
		return res
	}

	// Parse reference sent date
	var refDate time.Time
	var parseErr error

	formats := []string{
		time.RFC3339,
		time.RFC1123Z,
		time.RFC1123,
		time.RFC822Z,
		time.RFC822,
		"2006-01-02",
		"2006-01-02 15:04:05",
	}

	for _, f := range formats {
		refDate, parseErr = time.Parse(f, cleanSentDate)
		if parseErr == nil {
			break
		}
	}

	if parseErr != nil {
		res.Success = false
		res.Error = &GuidedErrorRecovery{
			Success:             false,
			ErrorCode:           "UNPARSEABLE_SENT_DATE_FORMAT",
			ErrorMessage:        fmt.Sprintf("Failed to parse email_sent_date '%s': %v.", cleanSentDate, parseErr),
			RecoveryGuidance:    "Supply a standard RFC3339 (e.g. 2026-09-04T10:00:00-07:00) or YYYY-MM-DD date string for email_sent_date.",
			SuggestedNextAction: "RETRY_WITH_ISO8601_DATE",
			ValidExamples:       []string{"2026-09-04T10:00:00Z", "2026-09-04"},
		}
		res.Guidance = res.Error.RecoveryGuidance
		return res
	}

	pLower := strings.ToLower(relativePhrase)
	var calculatedDate string

	if strings.Contains(pLower, "tomorrow") || strings.Contains(pLower, "next day") {
		calculatedDate = refDate.AddDate(0, 0, 1).Format("2006-01-02")
	} else if strings.Contains(pLower, "today") {
		calculatedDate = refDate.Format("2006-01-02")
	} else if strings.Contains(pLower, "in 2 days") || strings.Contains(pLower, "two days") {
		calculatedDate = refDate.AddDate(0, 0, 2).Format("2006-01-02")
	} else {
		// Fallback to storage helper
		deduced := storage.DeduceDeliveryDate(relativePhrase, &refDate)
		if deduced != "" {
			calculatedDate = deduced
		} else {
			res.Success = false
			res.Error = &GuidedErrorRecovery{
				Success:             false,
				ErrorCode:           "UNRECOGNIZED_RELATIVE_PHRASE",
				ErrorMessage:        fmt.Sprintf("Could not determine delivery offset from relative phrase '%s'.", relativePhrase),
				RecoveryGuidance:    "Identify whether the text mentions 'tomorrow', 'today', 'next day', or an explicit calendar date (YYYY-MM-DD). If an explicit date like 'September 8' or '2026-09-08' is stated in the email body, extract it directly into expected_delivery_date rather than using relative calculation.",
				SuggestedNextAction: "EXTRACT_EXPLICIT_CALENDAR_DATE",
				ValidExamples:       []string{"delivering tomorrow", "out for delivery today", "arriving next day"},
			}
			res.Guidance = res.Error.RecoveryGuidance
			return res
		}
	}

	res.Success = true
	res.Data = map[string]any{
		"expected_delivery_date": calculatedDate,
		"reference_sent_date":    refDate.Format("2006-01-02"),
		"relative_phrase":        relativePhrase,
	}
	res.Guidance = fmt.Sprintf("Relative delivery date deduced deterministically as %s based on email sent date (%s). Populate expected_delivery_date with this value.", calculatedDate, refDate.Format("2006-01-02"))
	return res
}

// ExecuteLookupExistingShipment searches for existing package records in the database.
func ExecuteLookupExistingShipment(ctx context.Context, store db.Store, userID, trackingNumber, carrier string) *ToolExecutionResult {
	res := &ToolExecutionResult{
		ToolName: "lookup_existing_shipment",
	}

	cleanUser := strings.TrimSpace(userID)
	if cleanUser == "" {
		res.Success = false
		res.Error = &GuidedErrorRecovery{
			Success:             false,
			ErrorCode:           "MISSING_USER_ID",
			ErrorMessage:        "user_id parameter is required for multi-tenant database lookup.",
			RecoveryGuidance:    "Supply the authenticated user's ID to ensure package data isolation.",
			SuggestedNextAction: "SUPPLY_AUTHENTICATED_USER_ID",
		}
		res.Guidance = res.Error.RecoveryGuidance
		return res
	}

	cleanTracking := strings.TrimSpace(trackingNumber)
	if cleanTracking == "" {
		res.Success = false
		res.Error = &GuidedErrorRecovery{
			Success:             false,
			ErrorCode:           "EMPTY_LOOKUP_TRACKING",
			ErrorMessage:        "tracking_number parameter cannot be empty for database lookup.",
			RecoveryGuidance:    "Provide the tracking number to search for existing package manifests.",
			SuggestedNextAction: "SUPPLY_TRACKING_NUMBER",
		}
		res.Guidance = res.Error.RecoveryGuidance
		return res
	}

	if store == nil {
		res.Success = true
		res.Data = map[string]any{
			"found":           false,
			"tracking_number": cleanTracking,
			"note":            "Database store not attached (offline verification mode).",
		}
		res.Guidance = "No database attached; treat as new package creation."
		return res
	}

	pkg, err := store.FindPackageByTrackingOrSender(ctx, cleanUser, cleanTracking, "")
	if err != nil {
		if errors.Is(err, db.ErrNotFound) || err.Error() == "record not found" {
			res.Success = true
			res.Data = map[string]any{
				"found":           false,
				"tracking_number": cleanTracking,
			}
			res.Guidance = "No existing package matches this tracking number. Create a new package document in user manifest."
			return res
		}
		res.Success = false
		res.Error = &GuidedErrorRecovery{
			Success:             false,
			ErrorCode:           "DATASTORE_LOOKUP_ERROR",
			ErrorMessage:        fmt.Sprintf("Failed querying datastore: %v.", err),
			RecoveryGuidance:    "Database lookup encountered an error. Proceed with cautious fallback as new package.",
			SuggestedNextAction: "PROCEED_AS_NEW_PACKAGE",
		}
		res.Guidance = res.Error.RecoveryGuidance
		return res
	}

	if pkg != nil {
		res.Success = true
		res.Data = map[string]any{
			"found":                  true,
			"package_id":             pkg.ID,
			"carrier":                pkg.Carrier,
			"current_status":         pkg.Status,
			"sender":                 pkg.Sender,
			"expected_delivery_date": pkg.ExpectedDeliveryDate,
		}
		res.Guidance = fmt.Sprintf("Existing package '%s' (ID: %s) found in user manifest with status '%s'. Reconcile inbound update with this package.", pkg.Sender, pkg.ID, pkg.Status)
	} else {
		res.Success = true
		res.Data = map[string]any{
			"found":           false,
			"tracking_number": cleanTracking,
		}
		res.Guidance = "No existing package matches this tracking number. Create a new package document in user manifest."
	}
	return res
}

// ExecuteReconcilePackageStatusTransition validates status progression and prevents invalid regressions.
func ExecuteReconcilePackageStatusTransition(currentStatus, newStatus string, hasException bool, carrierNotes string) *ToolExecutionResult {
	res := &ToolExecutionResult{
		ToolName: "reconcile_package_status_transition",
	}

	rank := map[string]int{
		"NONE":             0,
		"ORDERED":          1,
		"IN TRANSIT":       2,
		"OUT FOR DELIVERY": 3,
		"DELIVERED":        4,
		"EXCEPTION":        5,
	}

	curKey := strings.ToUpper(strings.TrimSpace(currentStatus))
	newKey := strings.ToUpper(strings.TrimSpace(newStatus))

	curRank, curOk := rank[curKey]
	if !curOk {
		curRank = 0
	}
	newRank, newOk := rank[newKey]
	if !newOk {
		res.Success = false
		res.Error = &GuidedErrorRecovery{
			Success:             false,
			ErrorCode:           "INVALID_PROPOSED_STATUS",
			ErrorMessage:        fmt.Sprintf("Proposed status '%s' is not recognized.", newStatus),
			RecoveryGuidance:    "Allowed package statuses are: 'Ordered', 'In Transit', 'Out for Delivery', 'Delivered', or 'Exception'.",
			SuggestedNextAction: "MAP_TO_STANDARD_STATUS",
			ValidExamples:       []string{"Ordered", "In Transit", "Out for Delivery", "Delivered", "Exception"},
		}
		res.Guidance = res.Error.RecoveryGuidance
		return res
	}

	// Regression check: Delivered to earlier status without exception
	if curRank == 4 && newRank < 4 && !hasException {
		res.Success = false
		res.Error = &GuidedErrorRecovery{
			Success:             false,
			ErrorCode:           "ILLEGAL_STATUS_REGRESSION",
			ErrorMessage:        fmt.Sprintf("Cannot regress package status from '%s' back to '%s' without an active exception flag.", currentStatus, newStatus),
			RecoveryGuidance:    "Packages that have reached 'Delivered' status cannot regress to earlier stages unless an explicit return-to-sender or shipping exception was detected (has_exception=true). Check if this email actually refers to a separate shipment or a return package.",
			SuggestedNextAction: "RETAIN_DELIVERED_STATUS_OR_FLAG_EXCEPTION",
			ValidExamples:       []string{"Retain 'Delivered' status", "Create new package for return shipment"},
		}
		res.Guidance = res.Error.RecoveryGuidance
		return res
	}

	res.Success = true
	res.Data = map[string]any{
		"transition_allowed": true,
		"previous_status":    currentStatus,
		"new_status":         newStatus,
		"is_progression":     newRank > curRank,
		"has_exception":      hasException,
	}
	res.Guidance = fmt.Sprintf("Status transition from '%s' to '%s' validated successfully.", currentStatus, newStatus)
	return res
}

// ExecuteSanitizeAndExtractItemNotes cleans item descriptions of PII and security tokens.
func ExecuteSanitizeAndExtractItemNotes(rawItemText string, maxLength int) *ToolExecutionResult {
	res := &ToolExecutionResult{
		ToolName: "sanitize_and_extract_item_notes",
	}

	if maxLength <= 0 {
		maxLength = 120
	}

	cleaned := security.RedactPII(rawItemText)
	cleaned = strings.TrimSpace(cleaned)

	if len(cleaned) > maxLength {
		cleaned = cleaned[:maxLength] + "..."
	}

	res.Success = true
	res.Data = map[string]any{
		"sanitized_notes":  cleaned,
		"original_length":  len(rawItemText),
		"sanitized_length": len(cleaned),
	}
	res.Guidance = "Item notes sanitized and stripped of sensitive data and credentials. Safe for presentation on package cards and briefings."
	return res
}

// DispatchTool dynamically routes a function call by name to its corresponding implementation.
func DispatchTool(ctx context.Context, store db.Store, toolName string, args map[string]interface{}) *ToolExecutionResult {
	switch toolName {
	case "validate_and_track_carrier_package":
		carrier, _ := args["carrier"].(string)
		tracking, _ := args["tracking_number"].(string)
		merchant, _ := args["merchant_name"].(string)
		return ExecuteValidateAndTrackCarrierPackage(carrier, tracking, merchant)

	case "calculate_relative_delivery_date":
		phrase, _ := args["relative_phrase"].(string)
		sentDate, _ := args["email_sent_date"].(string)
		tz, _ := args["target_timezone"].(string)
		return ExecuteCalculateRelativeDeliveryDate(phrase, sentDate, tz)

	case "lookup_existing_shipment":
		user, _ := args["user_id"].(string)
		tracking, _ := args["tracking_number"].(string)
		carrier, _ := args["carrier"].(string)
		return ExecuteLookupExistingShipment(ctx, store, user, tracking, carrier)

	case "reconcile_package_status_transition":
		current, _ := args["current_status"].(string)
		proposed, _ := args["new_status"].(string)
		hasExc, _ := args["has_exception"].(bool)
		notes, _ := args["carrier_notes"].(string)
		return ExecuteReconcilePackageStatusTransition(current, proposed, hasExc, notes)

	case "sanitize_and_extract_item_notes":
		raw, _ := args["raw_item_text"].(string)
		var maxLen int
		if ml, ok := args["max_length"].(float64); ok {
			maxLen = int(ml)
		}
		return ExecuteSanitizeAndExtractItemNotes(raw, maxLen)

	default:
		return &ToolExecutionResult{
			ToolName: toolName,
			Success:  false,
			Error: &GuidedErrorRecovery{
				Success:             false,
				ErrorCode:           "UNKNOWN_TOOL_FUNCTION",
				ErrorMessage:        fmt.Sprintf("Tool function '%s' is not defined in the agent tool registry.", toolName),
				RecoveryGuidance:    "Invoke only approved tools: 'validate_and_track_carrier_package', 'calculate_relative_delivery_date', 'lookup_existing_shipment', 'reconcile_package_status_transition', 'sanitize_and_extract_item_notes'.",
				SuggestedNextAction: "CALL_REGISTERED_TOOL",
				ValidExamples: []string{
					"validate_and_track_carrier_package",
					"calculate_relative_delivery_date",
					"lookup_existing_shipment",
					"reconcile_package_status_transition",
					"sanitize_and_extract_item_notes",
				},
			},
			Guidance: "Unknown tool requested.",
		}
	}
}

// ExecuteToolCall converts a genai.FunctionCall into a genai.FunctionResponse with structured recovery instructions.
func ExecuteToolCall(ctx context.Context, store db.Store, call *genai.FunctionCall) *genai.FunctionResponse {
	res := DispatchTool(ctx, store, call.Name, call.Args)
	return &genai.FunctionResponse{
		Name:     call.Name,
		Response: res.ResponseMap(),
	}
}
