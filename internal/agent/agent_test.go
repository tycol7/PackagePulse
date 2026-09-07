package agent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/tylerdean/package-tracker-demo/internal/agent"
	"github.com/tylerdean/package-tracker-demo/internal/db"
	"github.com/tylerdean/package-tracker-demo/internal/models"
	"github.com/tylerdean/package-tracker-demo/internal/storage"
)

func TestMockAgent_ClassificationGating(t *testing.T) {
	ctx := context.Background()
	ag := agent.NewMockAgent()

	// 1. Newsletter should be rejected
	newsletterEmail := storage.ParsedEmail{
		Subject: "Cloud Tech Weekly: 10 Trends in AI",
		From:    "newsletter@cloudtechweekly.com",
		Body:    "Top stories this week... click here to unsubscribe.",
	}
	res, err := ag.ProcessEmail(ctx, newsletterEmail)
	if err != nil {
		t.Fatalf("agent error: %v", err)
	}
	if res.IsPackageEmail {
		t.Errorf("expected newsletter to be rejected (is_package_email=false), got true")
	}
	if res.RejectionReason == "" {
		t.Errorf("expected rejection reason to be set")
	}

	// 2. FedEx email should be accepted
	shippingEmail := storage.ParsedEmail{
		Subject: "Your FedEx package is on its way! #773918274619",
		From:    "trackingupdates@fedex.com",
		Body:    "Your package from Google Store has shipped via FedEx. Tracking: 773918274619. Status: In Transit.",
	}
	resShip, err := ag.ProcessEmail(ctx, shippingEmail)
	if err != nil {
		t.Fatalf("agent error: %v", err)
	}
	if !resShip.IsPackageEmail {
		t.Errorf("expected shipping email to be accepted (is_package_email=true), got false")
	}
	if resShip.ExtractedPackage.TrackingNumber != "773918274619" {
		t.Errorf("expected tracking number 773918274619, got %s", resShip.ExtractedPackage.TrackingNumber)
	}
}

func TestReconciler_CreateAndStatusAdvance(t *testing.T) {
	ctx := context.Background()
	store := db.NewMemoryStore()
	reconciler := agent.NewReconciler(store)
	userID := "usr_googler_reconcile"

	// 1. First email: Package shipped (In Transit)
	fields1 := &models.ExtractedFields{
		Sender:               "Google Store",
		Carrier:              "FedEx",
		TrackingNumber:       "773918274619",
		TrackingLink:         "https://fedex.com/track/773918274619",
		ExpectedDeliveryDate: "2026-09-06",
		Status:               "In Transit",
		Notes:                "Pixel 9 Pro Fold",
	}

	pkg, isNew, err := reconciler.Reconcile(ctx, userID, "Your order shipped!", fields1)
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if !isNew {
		t.Errorf("expected isNew=true for first email")
	}
	if pkg.Status != models.StatusInTransit {
		t.Errorf("expected status In Transit, got %s", pkg.Status)
	}

	// Verify only 1 package in store
	pkgs, _ := store.ListPackages(ctx, userID, "")
	if len(pkgs) != 1 {
		t.Fatalf("expected 1 package in store, got %d", len(pkgs))
	}

	// 2. Second email: Same tracking number, advanced status (Out for Delivery)
	fields2 := &models.ExtractedFields{
		Sender:               "Google Store",
		Carrier:              "FedEx",
		TrackingNumber:       "773918274619",
		TrackingLink:         "https://fedex.com/track/773918274619",
		ExpectedDeliveryDate: "2026-09-06",
		Status:               "Out for Delivery",
		Notes:                "Pixel 9 Pro Fold (Driver is 3 stops away)",
	}

	pkgUpdated, isNew2, err := reconciler.Reconcile(ctx, userID, "Out for delivery today!", fields2)
	if err != nil {
		t.Fatalf("reconcile update failed: %v", err)
	}
	if isNew2 {
		t.Errorf("expected isNew=false for second email with matching tracking number")
	}
	if pkgUpdated.ID != pkg.ID {
		t.Errorf("expected same package ID %s, got %s", pkg.ID, pkgUpdated.ID)
	}
	if pkgUpdated.Status != models.StatusOutForDelivery {
		t.Errorf("expected status to advance to Out for Delivery, got %s", pkgUpdated.Status)
	}

	// Verify still exactly 1 package in store (no duplicates created)
	pkgsAfter, _ := store.ListPackages(ctx, userID, "")
	if len(pkgsAfter) != 1 {
		t.Errorf("expected exactly 1 package after update, got %d", len(pkgsAfter))
	}
}

func TestMockAgent_ComposeDigest(t *testing.T) {
	ctx := context.Background()
	ag := agent.NewMockAgent()
	user := &models.User{
		DisplayName: "Tyler Dean",
		Preferences: models.UserPreferences{Timezone: "America/Los_Angeles"},
	}

	pkgs := []*models.Package{
		{
			Sender:         "Google Store",
			Carrier:        "FedEx",
			TrackingNumber: "773918274619",
			Status:         models.StatusOutForDelivery,
			Notes:          "Pixel 9 Pro Fold",
		},
	}

	html, err := ag.ComposeDigest(ctx, user, pkgs)
	if err != nil {
		t.Fatalf("failed composing digest: %v", err)
	}
	if !strings.Contains(html, "Tyler Dean") {
		t.Errorf("expected digest to greet Tyler Dean")
	}
	if !strings.Contains(html, "773918274619") {
		t.Errorf("expected digest to contain tracking number")
	}
}

func TestMockAgent_MultiplePackagesExtraction(t *testing.T) {
	ctx := context.Background()
	ag := agent.NewMockAgent()

	multiPackageEmail := storage.ParsedEmail{
		Subject: "Your order has been split into 2 shipments",
		From:    "orders@google.com",
		Body: `Your Google Store order has shipped!
Shipment 1: Pixel 9 Pro Fold
Carrier: FedEx
Tracking: 773918274619

Shipment 2: Pixel Case and 45W USB-C Charger
Carrier: UPS
Tracking: 1Z9999999999999999`,
	}

	res, err := ag.ProcessEmail(ctx, multiPackageEmail)
	if err != nil {
		t.Fatalf("unexpected agent error: %v", err)
	}
	if !res.IsPackageEmail {
		t.Fatalf("expected is_package_email=true")
	}

	pkgs := res.AllPackages()
	if len(pkgs) != 2 {
		t.Fatalf("expected 2 extracted packages, got %d", len(pkgs))
	}

	if pkgs[0].TrackingNumber != "1Z9999999999999999" && pkgs[1].TrackingNumber != "1Z9999999999999999" {
		t.Errorf("expected to find UPS tracking 1Z9999999999999999 in packages")
	}
	if pkgs[0].TrackingNumber != "773918274619" && pkgs[1].TrackingNumber != "773918274619" {
		t.Errorf("expected to find FedEx tracking 773918274619 in packages")
	}
}

func TestReconciler_MultiPackageAndNullSanitization(t *testing.T) {
	ctx := context.Background()
	store := db.NewMemoryStore()
	reconciler := agent.NewReconciler(store)
	userID := "usr_multi_pkg_test"

	// Package 1 with literal "null" as tracking_link
	pkg1 := &models.ExtractedFields{
		Sender:               "Google Store",
		Carrier:              "FedEx",
		TrackingNumber:       "773918274619",
		TrackingLink:         "null",
		ExpectedDeliveryDate: "2026-09-08",
		Status:               "In Transit",
		Notes:                "null",
	}

	rec1, isNew1, err := reconciler.Reconcile(ctx, userID, "Shipment 1", pkg1)
	if err != nil {
		t.Fatalf("failed reconciling pkg1: %v", err)
	}
	if !isNew1 {
		t.Errorf("expected pkg1 to be new")
	}
	if rec1.TrackingLink != "" {
		t.Errorf("expected tracking_link 'null' to be sanitized to empty string, got: %q", rec1.TrackingLink)
	}
	if rec1.Notes != "" {
		t.Errorf("expected notes 'null' to be sanitized to empty string, got: %q", rec1.Notes)
	}

	// Package 2 from the same sender (Google Store) with a distinct tracking number
	pkg2 := &models.ExtractedFields{
		Sender:               "Google Store",
		Carrier:              "FedEx",
		TrackingNumber:       "773918274620",
		TrackingLink:         "https://www.fedex.com/fedextrack/?trknbr=773918274620",
		ExpectedDeliveryDate: "2026-09-09",
		Status:               "In Transit",
		Notes:                "Pixel Stand",
	}

	rec2, isNew2, err := reconciler.Reconcile(ctx, userID, "Shipment 2", pkg2)
	if err != nil {
		t.Fatalf("failed reconciling pkg2: %v", err)
	}
	if !isNew2 {
		t.Errorf("expected pkg2 to be new (distinct tracking number), not overwrite pkg1")
	}
	if rec2.ID == rec1.ID {
		t.Errorf("expected distinct package IDs, got same: %s", rec1.ID)
	}

	allPkgs, _ := store.ListPackages(ctx, userID, "")
	if len(allPkgs) != 2 {
		t.Fatalf("expected 2 packages in store, got %d", len(allPkgs))
	}
}

func TestMockAgent_TomorrowDeliveryDeductionAndReconciliation(t *testing.T) {
	ctx := context.Background()
	store := db.NewMemoryStore()
	reconciler := agent.NewReconciler(store)
	ag := agent.NewMockAgent()
	userID := "usr_tomorrow_deduction"

	// 1. Initial package created with default/earlier date
	initialPkg := &models.Package{
		ID:                   "pkg_initial_123",
		UserID:               userID,
		Sender:               "Google Store",
		Carrier:              "FedEx",
		TrackingNumber:       "773918274619",
		ExpectedDeliveryDate: "2026-09-10",
		Status:               models.StatusInTransit,
		Notes:                "Pixel 9 Pro",
	}
	_ = store.SavePackage(ctx, initialPkg)

	// 2. Inbound email stating delivery tomorrow, sent on 2026-09-04
	sentDate := "Fri, 04 Sep 2026 10:00:00 -0700"
	tomorrowEmail := storage.ExtractTextFromPayload("update.eml", []byte(
		"From: trackingupdates@fedex.com\n"+
			"Subject: FedEx Update: Your package will be delivered tomorrow!\n"+
			"Date: "+sentDate+"\n"+
			"Content-Type: text/plain\n\n"+
			"Hello, your Google Store shipment 773918274619 is on the move and will be delivered tomorrow.\n"+
			"Status: Out for Delivery\n",
	))

	res, err := ag.ProcessEmail(ctx, tomorrowEmail)
	if err != nil {
		t.Fatalf("unexpected agent error: %v", err)
	}
	if !res.IsPackageEmail {
		t.Fatalf("expected email to be classified as package email")
	}

	pkgFields := res.ExtractedPackage
	if pkgFields == nil {
		t.Fatalf("expected extracted package fields")
	}

	// Verify deduced date is 2026-09-05 (Sep 4 + 1 day)
	if pkgFields.ExpectedDeliveryDate != "2026-09-05" {
		t.Errorf("expected deduced delivery date 2026-09-05, got: %s", pkgFields.ExpectedDeliveryDate)
	}

	// 3. Reconcile with existing package
	reconciled, isNew, err := reconciler.Reconcile(ctx, userID, tomorrowEmail.Subject, pkgFields)
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if isNew {
		t.Errorf("expected existing package to be updated (isNew=false)")
	}
	if reconciled.ID != initialPkg.ID {
		t.Errorf("expected package ID %s, got %s", initialPkg.ID, reconciled.ID)
	}

	// Verify package in store has updated ExpectedDeliveryDate
	storedPkg, err := store.GetPackage(ctx, userID, initialPkg.ID)
	if err != nil {
		t.Fatalf("failed retrieving stored package: %v", err)
	}
	if storedPkg.ExpectedDeliveryDate != "2026-09-05" {
		t.Errorf("expected stored package expected delivery date to be updated to 2026-09-05, got: %s", storedPkg.ExpectedDeliveryDate)
	}
}

func TestMockAgent_MissingDateHeader_DoesNotGuess(t *testing.T) {
	ctx := context.Background()
	store := db.NewMemoryStore()
	reconciler := agent.NewReconciler(store)
	ag := agent.NewMockAgent()
	userID := "usr_no_date_header"

	// Existing package with an established delivery date
	existingPkg := &models.Package{
		ID:                   "pkg_nodate_check",
		UserID:               userID,
		Sender:               "Google Store",
		Carrier:              "FedEx",
		TrackingNumber:       "773918274619",
		ExpectedDeliveryDate: "2026-09-12",
		Status:               models.StatusInTransit,
		Notes:                "Pixel 9 Pro",
	}
	_ = store.SavePackage(ctx, existingPkg)

	// Inbound email mentioning tomorrow delivery, but completely missing a Date header
	nodateEmail := storage.ExtractTextFromPayload("update_nodate.eml", []byte(
		"From: trackingupdates@fedex.com\n"+
			"Subject: FedEx Update: Your package will be delivered tomorrow!\n"+
			"Content-Type: text/plain\n\n"+
			"Hello, your shipment 773918274619 is on the move and will be delivered tomorrow.\n"+
			"Status: Out for Delivery\n",
	))

	res, err := ag.ProcessEmail(ctx, nodateEmail)
	if err != nil {
		t.Fatalf("unexpected agent error: %v", err)
	}
	if !res.IsPackageEmail {
		t.Fatalf("expected email to be classified as package email")
	}

	pkgFields := res.ExtractedPackage
	if pkgFields == nil {
		t.Fatalf("expected extracted package fields")
	}

	// ExpectedDeliveryDate MUST be empty because no sent date is present to anchor "tomorrow"
	if pkgFields.ExpectedDeliveryDate != "" {
		t.Errorf("expected empty ExpectedDeliveryDate when Date header is absent, but got: %q", pkgFields.ExpectedDeliveryDate)
	}

	// Verify reasoning states date cannot be deduced without guessing
	if !strings.Contains(res.Reasoning, "cannot be deduced without guessing") {
		t.Errorf("expected reasoning to explain date cannot be deduced without guessing, got: %s", res.Reasoning)
	}

	// Reconcile with existing package
	reconciled, isNew, err := reconciler.Reconcile(ctx, userID, nodateEmail.Subject, pkgFields)
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if isNew {
		t.Errorf("expected existing package update")
	}

	// Verify existing package delivery date is preserved and not overwritten by an empty or guessed date
	if reconciled.ExpectedDeliveryDate != "2026-09-12" {
		t.Errorf("expected existing delivery date 2026-09-12 to be preserved, got: %s", reconciled.ExpectedDeliveryDate)
	}
}


