package db

import (
	"context"
	"errors"

	"github.com/tylerdean/package-tracker-demo/internal/models"
)

var (
	ErrNotFound      = errors.New("record not found")
	ErrAlreadyExists = errors.New("record already exists")
)

// Store defines the persistence operations for users and packages.
type Store interface {
	// User operations
	GetUser(ctx context.Context, id string) (*models.User, error)
	SaveUser(ctx context.Context, user *models.User) error
	ListUsersForDailyDigest(ctx context.Context) ([]*models.User, error)

	// Package operations
	GetPackage(ctx context.Context, userID, packageID string) (*models.Package, error)
	ListPackages(ctx context.Context, userID string, filterStatus models.PackageStatus) ([]*models.Package, error)
	SavePackage(ctx context.Context, pkg *models.Package) error
	UpdatePackage(ctx context.Context, pkg *models.Package) error
	DeletePackage(ctx context.Context, userID, packageID string) error
	FindPackageByTrackingOrSender(ctx context.Context, userID, trackingNumber, sender string) (*models.Package, error)
	ListPackagesArrivingToday(ctx context.Context, userID, dateStr string) ([]*models.Package, error)


	// Lifecycle
	Close() error
}
