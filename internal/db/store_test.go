package db_test

import (
	"context"
	"testing"
	"time"

	"github.com/tylerdean/package-tracker-demo/internal/db"
	"github.com/tylerdean/package-tracker-demo/internal/models"
)

func TestMemoryStore_MultiTenantIsolation(t *testing.T) {
	ctx := context.Background()
	store := db.NewMemoryStore()

	userA := "user_googler_A"
	userB := "user_googler_B"

	// Save package for User A
	pkgA := &models.Package{
		ID:             "pkg_101",
		UserID:         userA,
		Sender:         "Google Store",
		Carrier:        "FedEx",
		TrackingNumber: "773918274619",
		Status:         models.StatusInTransit,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	if err := store.SavePackage(ctx, pkgA); err != nil {
		t.Fatalf("failed saving package: %v", err)
	}

	// Verify User A sees package
	pkgsA, err := store.ListPackages(ctx, userA, "")
	if err != nil || len(pkgsA) != 1 {
		t.Fatalf("expected User A to see 1 package, got %d (err: %v)", len(pkgsA), err)
	}

	// Verify User B does NOT see User A's package (multi-tenant isolation)
	pkgsB, err := store.ListPackages(ctx, userB, "")
	if err != nil || len(pkgsB) != 0 {
		t.Fatalf("expected User B to see 0 packages, got %d", len(pkgsB))
	}

	// Verify User B cannot fetch User A's package directly by ID
	_, err = store.GetPackage(ctx, userB, "pkg_101")
	if err != db.ErrNotFound {
		t.Errorf("expected ErrNotFound for User B accessing User A package, got: %v", err)
	}
}

func TestMemoryStore_FindPackageByTrackingOrSender(t *testing.T) {
	ctx := context.Background()
	store := db.NewMemoryStore()
	userID := "usr_test_123"

	pkg := &models.Package{
		ID:             "pkg_202",
		UserID:         userID,
		Sender:         "Apple Store",
		Carrier:        "UPS",
		TrackingNumber: "1Z9999999999999999",
		Status:         models.StatusOrdered,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	_ = store.SavePackage(ctx, pkg)

	// Match by tracking number
	found, err := store.FindPackageByTrackingOrSender(ctx, userID, "1Z9999999999999999", "")
	if err != nil || found == nil || found.ID != "pkg_202" {
		t.Errorf("expected to find package by tracking number, got: %v", found)
	}

	// Match by sender
	foundBySender, err := store.FindPackageByTrackingOrSender(ctx, userID, "", "apple store")
	if err != nil || foundBySender == nil || foundBySender.ID != "pkg_202" {
		t.Errorf("expected to find package by sender, got: %v", foundBySender)
	}
}

func TestMemoryStore_ListPackagesArrivingToday(t *testing.T) {
	ctx := context.Background()
	store := db.NewMemoryStore()
	userID := "usr_test_today"
	today := "2026-09-06"
	tomorrow := "2026-09-07"

	pkgToday := &models.Package{
		ID:                   "pkg_today",
		UserID:               userID,
		Sender:               "Google Store",
		ExpectedDeliveryDate: today,
		Status:               models.StatusOutForDelivery,
	}
	pkgTomorrow := &models.Package{
		ID:                   "pkg_tomorrow",
		UserID:               userID,
		Sender:               "Amazon",
		ExpectedDeliveryDate: tomorrow,
		Status:               models.StatusInTransit,
	}

	_ = store.SavePackage(ctx, pkgToday)
	_ = store.SavePackage(ctx, pkgTomorrow)

	arriving, err := store.ListPackagesArrivingToday(ctx, userID, today)
	if err != nil || len(arriving) != 1 || arriving[0].ID != "pkg_today" {
		t.Errorf("expected 1 arriving today, got: %v", arriving)
	}
}
