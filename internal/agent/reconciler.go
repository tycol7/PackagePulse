package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/tylerdean/package-tracker-demo/internal/db"
	"github.com/tylerdean/package-tracker-demo/internal/models"
)

// Reconciler handles matching, updating, or inserting package records based on agent extraction.
type Reconciler struct {
	store db.Store
}

func NewReconciler(store db.Store) *Reconciler {
	return &Reconciler{store: store}
}

// Reconcile applies the agent extraction result to Firestore.
// The agent always overwrites existing data if a matching package is found.
func (r *Reconciler) Reconcile(ctx context.Context, userID string, subject string, fields *models.ExtractedFields) (*models.Package, bool, error) {
	if fields == nil {
		return nil, false, errors.New("cannot reconcile nil package fields")
	}
	fields.Sanitize()

	// 1. Search for existing package by tracking number or by sender
	existing, err := r.store.FindPackageByTrackingOrSender(ctx, userID, fields.TrackingNumber, fields.Sender)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return nil, false, fmt.Errorf("failed searching existing package: %w", err)
	}

	now := time.Now().UTC()

	// 2. If existing package found -> OVERWRITE with newest status and details
	if existing != nil {
		existing.TrackingLink = models.SanitizeCleanURL(existing.TrackingLink)
		if fields.Status != "" {
			existing.Status = models.PackageStatus(fields.Status)
		}
		if fields.ExpectedDeliveryDate != "" {
			existing.ExpectedDeliveryDate = fields.ExpectedDeliveryDate
		}
		if fields.TrackingNumber != "" {
			existing.TrackingNumber = fields.TrackingNumber
		}
		if fields.TrackingLink != "" {
			existing.TrackingLink = fields.TrackingLink
		}
		if fields.Carrier != "" {
			existing.Carrier = fields.Carrier
		}
		if fields.Notes != "" {
			existing.Notes = fields.Notes
		}
		existing.RawEmailSubject = subject
		existing.UpdatedAt = now

		if err := r.store.UpdatePackage(ctx, existing); err != nil {
			return nil, false, fmt.Errorf("failed updating existing package: %w", err)
		}
		return existing, false, nil // false = updated existing
	}

	// 3. New package -> INSERT new document
	pkgStatus := models.StatusInTransit
	if fields.Status != "" {
		pkgStatus = models.PackageStatus(fields.Status)
	}

	newPackage := &models.Package{
		ID:                   generatePackageID(),
		UserID:               userID,
		Sender:               fields.Sender,
		Carrier:              fields.Carrier,
		TrackingNumber:       fields.TrackingNumber,
		TrackingLink:         fields.TrackingLink,
		ExpectedDeliveryDate: fields.ExpectedDeliveryDate,
		Status:               pkgStatus,
		Notes:                fields.Notes,
		Source:               "EMAIL_UPLOAD",
		RawEmailSubject:      subject,
		CreatedAt:            now,
		UpdatedAt:            now,
	}

	if err := r.store.SavePackage(ctx, newPackage); err != nil {
		return nil, false, fmt.Errorf("failed saving new package: %w", err)
	}

	return newPackage, true, nil // true = created new
}

func generatePackageID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "pkg_" + hex.EncodeToString(b)
}
