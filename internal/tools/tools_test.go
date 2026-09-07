package tools_test

import (
	"context"
	"strings"
	"testing"

	"cloud.google.com/go/vertexai/genai"
	"github.com/tylerdean/package-tracker-demo/internal/db"
	"github.com/tylerdean/package-tracker-demo/internal/models"
	"github.com/tylerdean/package-tracker-demo/internal/tools"
)

// TestToolDeclarations_ComprehensiveDocstringsAndDescriptiveNaming verifies that
// every callable tool has descriptive naming and comprehensive docstrings for the LLM.
func TestToolDeclarations_ComprehensiveDocstringsAndDescriptiveNaming(t *testing.T) {
	decls := tools.GetFunctionDeclarations()
	if len(decls) != 5 {
		t.Fatalf("expected 5 tool declarations, got %d", len(decls))
	}

	expectedTools := map[string]struct {
		minDescLength int
		requiredParams []string
	}{
		"validate_and_track_carrier_package": {
			minDescLength: 80,
			requiredParams: []string{"carrier", "tracking_number"},
		},
		"calculate_relative_delivery_date": {
			minDescLength: 80,
			requiredParams: []string{"relative_phrase", "email_sent_date"},
		},
		"lookup_existing_shipment": {
			minDescLength: 80,
			requiredParams: []string{"user_id", "tracking_number"},
		},
		"reconcile_package_status_transition": {
			minDescLength: 80,
			requiredParams: []string{"current_status", "new_status"},
		},
		"sanitize_and_extract_item_notes": {
			minDescLength: 80,
			requiredParams: []string{"raw_item_text"},
		},
	}

	for _, decl := range decls {
		exp, ok := expectedTools[decl.Name]
		if !ok {
			t.Errorf("unexpected tool name: %s", decl.Name)
			continue
		}

		// Verify descriptive tool docstring
		if len(strings.TrimSpace(decl.Description)) < exp.minDescLength {
			t.Errorf("tool '%s' description is too short (%d chars, min %d): %q",
				decl.Name, len(decl.Description), exp.minDescLength, decl.Description)
		}

		if decl.Parameters == nil || decl.Parameters.Type != genai.TypeObject {
			t.Errorf("tool '%s' must declare TypeObject parameters schema", decl.Name)
			continue
		}

		// Verify every parameter has a comprehensive docstring
		for paramName, paramSchema := range decl.Parameters.Properties {
			if len(strings.TrimSpace(paramSchema.Description)) < 25 {
				t.Errorf("tool '%s' parameter '%s' has insufficient docstring (%d chars): %q",
					decl.Name, paramName, len(paramSchema.Description), paramSchema.Description)
			}
		}

		// Verify required parameters are declared
		for _, req := range exp.requiredParams {
			found := false
			for _, r := range decl.Parameters.Required {
				if r == req {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("tool '%s' missing required parameter declaration: '%s'", decl.Name, req)
			}
		}
	}
}

// TestGuidedErrorRecovery_ValidationFailures verifies that all tool failures return
// structured, actionable error recovery instructions for the model.
func TestGuidedErrorRecovery_ValidationFailures(t *testing.T) {
	ctx := context.Background()

	// 1. validate_and_track_carrier_package: Empty tracking number
	resEmpty := tools.ExecuteValidateAndTrackCarrierPackage("FedEx", "", "Google Store")
	if resEmpty.Success {
		t.Errorf("expected failure for empty tracking number")
	}
	if resEmpty.Error == nil || resEmpty.Error.ErrorCode != "EMPTY_TRACKING_NUMBER" {
		t.Errorf("expected EMPTY_TRACKING_NUMBER error code, got %v", resEmpty.Error)
	}
	if len(resEmpty.Error.RecoveryGuidance) < 30 || resEmpty.Error.SuggestedNextAction == "" || len(resEmpty.Error.ValidExamples) == 0 {
		t.Errorf("incomplete guided error recovery on empty tracking: %+v", resEmpty.Error)
	}

	// 2. validate_and_track_carrier_package: Malformed FedEx tracking (5 digits instead of 12)
	resFedexBad := tools.ExecuteValidateAndTrackCarrierPackage("FedEx", "12345", "Google Store")
	if resFedexBad.Success {
		t.Errorf("expected failure for invalid FedEx tracking length")
	}
	if resFedexBad.Error == nil || resFedexBad.Error.ErrorCode != "INVALID_FEDEX_TRACKING_LENGTH" {
		t.Errorf("expected INVALID_FEDEX_TRACKING_LENGTH, got %v", resFedexBad.Error)
	}
	if !strings.Contains(resFedexBad.Error.RecoveryGuidance, "12-digit") {
		t.Errorf("expected recovery guidance to advise 12-digit search, got %s", resFedexBad.Error.RecoveryGuidance)
	}

	// 3. validate_and_track_carrier_package: Malformed UPS tracking (missing 1Z)
	resUPSBad := tools.ExecuteValidateAndTrackCarrierPackage("UPS", "999999999999", "Amazon")
	if resUPSBad.Success {
		t.Errorf("expected failure for invalid UPS tracking format")
	}
	if resUPSBad.Error == nil || resUPSBad.Error.ErrorCode != "INVALID_UPS_TRACKING_FORMAT" {
		t.Errorf("expected INVALID_UPS_TRACKING_FORMAT, got %v", resUPSBad.Error)
	}

	// 4. calculate_relative_delivery_date: Missing reference Sent Date
	resNoDate := tools.ExecuteCalculateRelativeDeliveryDate("delivering tomorrow", "", "UTC")
	if resNoDate.Success {
		t.Errorf("expected failure when Sent Date is missing")
	}
	if resNoDate.Error == nil || resNoDate.Error.ErrorCode != "MISSING_REFERENCE_SENT_DATE" {
		t.Errorf("expected MISSING_REFERENCE_SENT_DATE, got %v", resNoDate.Error)
	}
	if !strings.Contains(resNoDate.Error.RecoveryGuidance, "STRICT SAFETY RULE") {
		t.Errorf("expected strict safety guidance forbidding guessing, got %s", resNoDate.Error.RecoveryGuidance)
	}

	// 5. reconcile_package_status_transition: Illegal regression from Delivered to Ordered
	resRegress := tools.ExecuteReconcilePackageStatusTransition("Delivered", "Ordered", false, "")
	if resRegress.Success {
		t.Errorf("expected failure for illegal status regression")
	}
	if resRegress.Error == nil || resRegress.Error.ErrorCode != "ILLEGAL_STATUS_REGRESSION" {
		t.Errorf("expected ILLEGAL_STATUS_REGRESSION, got %v", resRegress.Error)
	}
	if resRegress.Error.SuggestedNextAction == "" || len(resRegress.Error.ValidExamples) == 0 {
		t.Errorf("expected actionable recovery instructions for illegal regression, got %+v", resRegress.Error)
	}

	// 6. Unknown tool name dispatch
	resUnknown := tools.DispatchTool(ctx, nil, "non_existent_tool", nil)
	if resUnknown.Success || resUnknown.Error.ErrorCode != "UNKNOWN_TOOL_FUNCTION" {
		t.Errorf("expected UNKNOWN_TOOL_FUNCTION, got %+v", resUnknown)
	}
}

// TestToolExecutions_SuccessfulCases verifies that valid inputs succeed and return
// clean data and next-step continuation guidance.
func TestToolExecutions_SuccessfulCases(t *testing.T) {
	ctx := context.Background()
	store := db.NewMemoryStore()
	userID := "usr_eval_test"

	// 1. validate_and_track_carrier_package
	resFedex := tools.ExecuteValidateAndTrackCarrierPackage("FedEx", "773918274619", "Google Store")
	if !resFedex.Success {
		t.Fatalf("expected FedEx validation success, got error: %+v", resFedex.Error)
	}
	if resFedex.Data["tracking_link"] != "https://www.fedex.com/fedextrack/?trknbr=773918274619" {
		t.Errorf("incorrect tracking link: %v", resFedex.Data["tracking_link"])
	}

	resUPS := tools.ExecuteValidateAndTrackCarrierPackage("UPS", "1Z9999999999999999", "Amazon")
	if !resUPS.Success {
		t.Fatalf("expected UPS validation success, got error: %+v", resUPS.Error)
	}
	if resUPS.Data["tracking_link"] != "https://www.ups.com/track?tracknum=1Z9999999999999999" {
		t.Errorf("incorrect UPS tracking link: %v", resUPS.Data["tracking_link"])
	}

	// 2. calculate_relative_delivery_date
	resDate := tools.ExecuteCalculateRelativeDeliveryDate("delivering tomorrow", "2026-09-04T10:00:00-07:00", "UTC")
	if !resDate.Success {
		t.Fatalf("expected date calculation success, got error: %+v", resDate.Error)
	}
	if resDate.Data["expected_delivery_date"] != "2026-09-05" {
		t.Errorf("expected 2026-09-05, got %v", resDate.Data["expected_delivery_date"])
	}

	// 3. lookup_existing_shipment
	// Initially not found
	resLookup1 := tools.ExecuteLookupExistingShipment(ctx, store, userID, "773918274619", "FedEx")
	if !resLookup1.Success || resLookup1.Data["found"] != false {
		t.Errorf("expected not found, got %+v", resLookup1)
	}

	// Insert package and lookup again
	_ = store.SavePackage(ctx, &models.Package{
		ID:             "pkg_123",
		UserID:         userID,
		TrackingNumber: "773918274619",
		Carrier:        "FedEx",
		Status:         "In Transit",
		Sender:         "Google Store",
	})
	resLookup2 := tools.ExecuteLookupExistingShipment(ctx, store, userID, "773918274619", "FedEx")
	if !resLookup2.Success || resLookup2.Data["found"] != true || resLookup2.Data["package_id"] != "pkg_123" {
		t.Errorf("expected package to be found, got %+v", resLookup2)
	}

	// 4. reconcile_package_status_transition: Valid forward progression
	resProg := tools.ExecuteReconcilePackageStatusTransition("In Transit", "Out for Delivery", false, "")
	if !resProg.Success || resProg.Data["transition_allowed"] != true || resProg.Data["is_progression"] != true {
		t.Errorf("expected valid status progression, got %+v", resProg)
	}

	// 5. sanitize_and_extract_item_notes
	resNotes := tools.ExecuteSanitizeAndExtractItemNotes("Purchased Pixel 9 Pro. Deliver to tylerdean@google.com, (555) 234-5678, Card 4111 2222 3333 4444", 100)
	if !resNotes.Success {
		t.Fatalf("expected notes sanitization success, got error: %+v", resNotes.Error)
	}
	sanitizedStr, ok := resNotes.Data["sanitized_notes"].(string)
	if !ok || strings.Contains(sanitizedStr, "tylerdean@google.com") || strings.Contains(sanitizedStr, "4111") {
		t.Errorf("expected PII to be redacted from sanitized notes: %s", sanitizedStr)
	}

	// 6. ExecuteToolCall wrapper for genai.FunctionCall
	fnCall := &genai.FunctionCall{
		Name: "calculate_relative_delivery_date",
		Args: map[string]any{
			"relative_phrase": "tomorrow",
			"email_sent_date": "2026-09-08",
		},
	}
	fnResp := tools.ExecuteToolCall(ctx, store, fnCall)
	if fnResp.Name != "calculate_relative_delivery_date" {
		t.Errorf("expected function response name match")
	}
	if fnResp.Response["success"] != true {
		t.Errorf("expected function response success, got %+v", fnResp.Response)
	}
}
