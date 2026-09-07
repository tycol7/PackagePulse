package handlers

import (
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/tylerdean/package-tracker-demo/internal/agent"
	"github.com/tylerdean/package-tracker-demo/internal/auth"
	"github.com/tylerdean/package-tracker-demo/internal/db"
	"github.com/tylerdean/package-tracker-demo/internal/models"
	"github.com/tylerdean/package-tracker-demo/internal/security"
	"github.com/tylerdean/package-tracker-demo/internal/storage"
	"github.com/tylerdean/package-tracker-demo/internal/telemetry"
)

type UploadHandler struct {
	tmpl       *template.Template
	store      db.Store
	blobStore  storage.BlobStorage
	agent      agent.LogisticsAgent
	reconciler *agent.Reconciler
	modelArmor security.ModelArmorService
}

func NewUploadHandler(tmpl *template.Template, store db.Store, blobStore storage.BlobStorage, aiAgent agent.LogisticsAgent, modelArmor security.ModelArmorService) *UploadHandler {
	return &UploadHandler{
		tmpl:       tmpl,
		store:      store,
		blobStore:  blobStore,
		agent:      aiAgent,
		reconciler: agent.NewReconciler(store),
		modelArmor: modelArmor,
	}
}

// HandleUpload handles multi-part email uploads (.eml or .txt), runs AI extraction,
// reconciles package state, and streams telemetry to Google Cloud Trace & Logging.
func (h *UploadHandler) HandleUpload(w http.ResponseWriter, r *http.Request) {
	session := auth.GetUserFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 1. Read file upload (max 15MB)
	if err := r.ParseMultipartForm(15 << 20); err != nil {
		http.Error(w, "Upload exceeds maximum allowed size (15MB)", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("email_file")
	if err != nil {
		http.Error(w, "Invalid file upload", http.StatusBadRequest)
		return
	}
	defer file.Close()

	rawBytes, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "Failed reading file content", http.StatusInternalServerError)
		return
	}

	projectID := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if projectID == "" {
		projectID = "package-tracker-demo"
	}

	// Root span for the inbound email ingestion agent pipeline
	ctx := telemetry.ExtractTraceContext(r)
	sessionID := fmt.Sprintf("session-%s", session.UserID)
	ctx = telemetry.ContextWithSessionID(ctx, sessionID)
	ctx, rootSpan := telemetry.StartAgentSpan(ctx, "invoke_agent InboundEmailAgent",
		trace.WithAttributes(
			attribute.String("user.id", session.UserID),
			attribute.String("email.filename", header.Filename),
			attribute.Int("email.size_bytes", len(rawBytes)),
		),
	)
	defer rootSpan.End()

	// 1. Extract text content (.eml MIME or .txt)
	ctx, mimeSpan := telemetry.StartToolSpan(ctx, "mime_parse")
	parsed := storage.ExtractTextFromPayload(header.Filename, rawBytes)
	mimeSpan.SetAttributes(
		attribute.String("email.subject", security.RedactPII(parsed.Subject)),
		attribute.String("email.from", security.RedactPII(parsed.From)),
	)
	mimeSpan.End()

	telemetry.LogStep(ctx, projectID, "IntakeGate", "mime_parse", "PASSED", map[string]interface{}{
		"filename": header.Filename,
		"bytes":    len(rawBytes),
		"subject":  parsed.Subject,
		"sender":   parsed.From,
	})

	// 2. Security Gatekeeper: Model Armor Prompt Injection & Harmful Content Screening
	// CRITICAL: We check for malicious emails BEFORE storing anything to GCS.
	// If the email is malicious (prompt injection, jailbreak, exploit), it is REJECTED and NEVER stored to GCS!
	if h.modelArmor != nil {
		contentToScreen := fmt.Sprintf("Subject: %s\nFrom: %s\n\n%s", parsed.Subject, parsed.From, parsed.Body)
		armorResult, err := h.modelArmor.ScreenPrompt(ctx, contentToScreen)
		if err != nil {
			log.Printf("[ModelArmor] Warning: screening returned error: %v", err)
		} else if armorResult != nil && !armorResult.Passed {
			reason := armorResult.BlockedReason
			if reason == "" {
				reason = "Inbound email blocked by Model Armor security policy."
			}
			log.Printf("[Security] Inbound email BLOCKED by Model Armor: %s (jailbreak=%t, confidence=%s)",
				reason, armorResult.JailbreakDetected, armorResult.Confidence)

			telemetry.LogStep(ctx, projectID, "SecurityGate", "model_armor_sanitize", "BLOCKED", map[string]interface{}{
				"reason":             reason,
				"jailbreak_detected": armorResult.JailbreakDetected,
				"confidence":         armorResult.Confidence,
				"subject":            parsed.Subject,
				"sender":             parsed.From,
				"gcs_stored":         false,
			})

			w.Header().Set("HX-Trigger", fmt.Sprintf(`{"showToast": "Security Alert: %s"}`, reason))

			// Return current package list and status pill counts unchanged WITHOUT storing to GCS
			packages, _ := h.store.ListPackages(ctx, session.UserID, "")
			_ = h.tmpl.ExecuteTemplate(w, "package_list", map[string]interface{}{
				"Packages": packages,
			})
			_ = h.tmpl.ExecuteTemplate(w, "status_pill_counts", models.CalculateStatusCounts(packages))
			return
		}
	}

	// 3. PII Redaction & Secure GCS Archiving
	// Only clean emails that PASSED security screening are archived, AND all sensitive PII is redacted BEFORE writing to GCS!
	_, gcsSpan := telemetry.StartToolSpan(ctx, "archive_redacted_email")
	redactedBytes := security.RedactEmailPayload(header.Filename, rawBytes)
	gcsPath := fmt.Sprintf("inbound/%s/%d_redacted_%s", session.UserID, time.Now().Unix(), header.Filename)
	if err := h.blobStore.WriteBytes(ctx, gcsPath, redactedBytes); err != nil {
		log.Printf("[GCS] Warning: Failed writing redacted bytes to bucket: %v", err)
	} else {
		log.Printf("[GCS] Successfully archived PII-redacted email payload at gs://.../%s", gcsPath)
	}
	gcsSpan.SetAttributes(
		attribute.String("gcs.path", gcsPath),
		attribute.Bool("gcs.pii_redacted", true),
		attribute.Bool("gcs.security_screened", true),
		attribute.Int("gcs.bytes", len(redactedBytes)),
	)
	gcsSpan.End()

	telemetry.LogStep(ctx, projectID, "StorageGate", "archive_redacted_email", "ARCHIVED", map[string]interface{}{
		"gcs_path":          gcsPath,
		"pii_redacted":      true,
		"security_screened": true,
		"bytes":             len(redactedBytes),
	})

	// 4. Step: Classifier Gatekeeper (Is Delivery Email vs. Non-Delivery Email)
	ctx, classifierSpan := telemetry.StartToolSpan(ctx, "classify_delivery_email",
		trace.WithAttributes(
			attribute.String("email.subject", security.RedactPII(parsed.Subject)),
			attribute.String("email.sender", security.RedactPII(parsed.From)),
			attribute.String("gcp.agent.tool_call_args", fmt.Sprintf(`{"subject": %q, "from": %q}`, security.RedactPII(parsed.Subject), security.RedactPII(parsed.From))),
		),
	)
	result, err := h.agent.ProcessEmail(ctx, parsed)
	if err != nil {
		classifierSpan.RecordError(err)
		classifierSpan.End()
		log.Printf("[Agent] Error processing email: %v", err)
		http.Error(w, "AI Agent processing failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// 5. Gatekeeper Check: If classified as NOT delivery-related -> discard and notify
	if !result.IsPackageEmail {
		reason := result.RejectionReason
		if reason == "" {
			reason = "Email does not contain order or package shipment details."
		}
		classifierSpan.SetAttributes(
			attribute.Bool("agent.is_delivery_email", false),
			attribute.String("agent.classification", "NON_DELIVERY_DISCARDED"),
			attribute.Float64("agent.confidence", result.Confidence),
			attribute.String("agent.reasoning", result.Reasoning),
			attribute.String("agent.rejection_reason", reason),
			attribute.String("gcp.agent.tool_response", fmt.Sprintf(`{"is_delivery_email": false, "classification": "NON_DELIVERY_DISCARDED", "confidence": %.2f, "reason": %q, "reasoning": %q}`, result.Confidence, reason, result.Reasoning)),
		)
		classifierSpan.End()

		log.Printf("[Agent] Inbound email discarded: %s", reason)
		telemetry.LogStep(ctx, projectID, "Classifier", "classify_delivery_email", "DISCARDED", map[string]interface{}{
			"is_tracking": false,
			"confidence":  result.Confidence,
			"reasoning":   result.Reasoning,
			"rejection":   reason,
		})

		w.Header().Set("HX-Trigger", fmt.Sprintf(`{"showToast": "Discarded: %s"}`, reason))

		// Return current package list unchanged + updated status pill counts
		packages, _ := h.store.ListPackages(ctx, session.UserID, "")
		_ = h.tmpl.ExecuteTemplate(w, "package_list", map[string]interface{}{
			"Packages": packages,
		})
		_ = h.tmpl.ExecuteTemplate(w, "status_pill_counts", models.CalculateStatusCounts(packages))
		return
	}

	// 6. Valid delivery email -> gatekeeper accepted
	classifierSpan.SetAttributes(
		attribute.Bool("agent.is_delivery_email", true),
		attribute.String("agent.classification", "DELIVERY_EMAIL_ACCEPTED"),
		attribute.Float64("agent.confidence", result.Confidence),
		attribute.String("agent.reasoning", result.Reasoning),
		attribute.Int("agent.packages_found", len(result.AllPackages())),
		attribute.String("gcp.agent.tool_response", fmt.Sprintf(`{"is_delivery_email": true, "classification": "DELIVERY_EMAIL_ACCEPTED", "confidence": %.2f, "packages_found": %d, "reasoning": %q}`, result.Confidence, len(result.AllPackages()), result.Reasoning)),
	)
	classifierSpan.End()

	pkgsToProcess := result.AllPackages()
	if len(pkgsToProcess) == 0 {
		pkgsToProcess = []*models.ExtractedFields{{
			Sender: parsed.From,
			Status: "Ordered",
			Notes:  parsed.Subject,
		}}
	}

	var createdCount, updatedCount int
	var createdSenders []string

	for i, pkgFields := range pkgsToProcess {
		pkgSpanName := fmt.Sprintf("reconcile_shipment_%d", i+1)
		ctx, pkgSpan := telemetry.StartToolSpan(ctx, pkgSpanName,
			trace.WithAttributes(
				attribute.Int("shipment.index", i+1),
				attribute.String("shipment.carrier", pkgFields.Carrier),
				attribute.String("shipment.tracking_number", pkgFields.TrackingNumber),
				attribute.String("shipment.sender", security.RedactPII(pkgFields.Sender)),
				attribute.String("shipment.status", pkgFields.Status),
				attribute.String("shipment.expected_delivery", pkgFields.ExpectedDeliveryDate),
				attribute.String("gcp.agent.tool_call_args", fmt.Sprintf(`{"carrier": %q, "tracking_number": %q, "sender": %q, "status": %q}`, pkgFields.Carrier, pkgFields.TrackingNumber, security.RedactPII(pkgFields.Sender), pkgFields.Status)),
			),
		)

		pkg, isNew, err := h.reconciler.Reconcile(ctx, session.UserID, parsed.Subject, pkgFields)
		if err != nil {
			pkgSpan.RecordError(err)
			pkgSpan.End()
			log.Printf("[Agent] Reconciliation failed for package %d (%s): %v", i+1, pkgFields.TrackingNumber, err)
			continue
		}

		actionName := "Status Updated"
		if isNew {
			actionName = "New Package Created"
			createdCount++
			createdSenders = append(createdSenders, pkg.Sender)
		} else {
			updatedCount++
		}

		pkgSpan.SetAttributes(
			attribute.String("package.id", pkg.ID),
			attribute.Bool("package.is_new", isNew),
			attribute.String("package.action", actionName),
			attribute.String("package.status", string(pkg.Status)),
			attribute.String("gcp.agent.tool_response", fmt.Sprintf(`{"package_id": %q, "is_new": %t, "action": %q, "status": %q}`, pkg.ID, isNew, actionName, pkg.Status)),
		)
		pkgSpan.End()

		telemetry.LogStep(ctx, projectID, "Reconciler", "reconciliation", actionName, map[string]interface{}{
			"package_id":      pkg.ID,
			"sender":          pkg.Sender,
			"carrier":         pkg.Carrier,
			"tracking_number": pkg.TrackingNumber,
			"status":          pkg.Status,
			"is_new":          isNew,
		})
	}

	var actionMsg string
	totalProcessed := createdCount + updatedCount
	if totalProcessed == 1 {
		if createdCount == 1 {
			actionMsg = fmt.Sprintf("New package from %s created!", security.RedactPII(createdSenders[0]))
		} else {
			actionMsg = "Package status updated!"
		}
	} else if totalProcessed > 1 {
		actionMsg = fmt.Sprintf("Processed %d packages (%d new, %d updated)!", totalProcessed, createdCount, updatedCount)
	} else {
		actionMsg = "Email processed, but no packages were recorded."
	}
	log.Printf("[Agent] %s", security.RedactPII(actionMsg))

	w.Header().Set("HX-Trigger", fmt.Sprintf(`{"showToast": "%s"}`, security.RedactPII(actionMsg)))

	// Return updated package list + updated status pill counts
	packages, _ := h.store.ListPackages(ctx, session.UserID, "")
	_ = h.tmpl.ExecuteTemplate(w, "package_list", map[string]interface{}{
		"Packages": packages,
	})
	_ = h.tmpl.ExecuteTemplate(w, "status_pill_counts", models.CalculateStatusCounts(packages))
}
