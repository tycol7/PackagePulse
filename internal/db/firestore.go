package db

import (
	"context"
	"fmt"
	"strings"

	"cloud.google.com/go/firestore"
	"github.com/tylerdean/package-tracker-demo/internal/models"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// FirestoreStore implements Store using Google Cloud Firestore Native Mode.
type FirestoreStore struct {
	client *firestore.Client
}

// NewFirestoreStore initializes a new Firestore client.
func NewFirestoreStore(ctx context.Context, projectID, databaseID string) (*FirestoreStore, error) {
	if databaseID == "" {
		databaseID = "(default)"
	}
	client, err := firestore.NewClientWithDatabase(ctx, projectID, databaseID)
	if err != nil {
		return nil, fmt.Errorf("failed to create firestore client: %w", err)
	}
	return &FirestoreStore{client: client}, nil
}

func (s *FirestoreStore) Close() error {
	return s.client.Close()
}

// --- User Operations ---

func (s *FirestoreStore) GetUser(ctx context.Context, id string) (*models.User, error) {
	docRef := s.client.Collection("users").Doc(id)
	snap, err := docRef.Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var u models.User
	if err := snap.DataTo(&u); err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *FirestoreStore) SaveUser(ctx context.Context, user *models.User) error {
	docRef := s.client.Collection("users").Doc(user.ID)
	_, err := docRef.Set(ctx, user)
	return err
}

func (s *FirestoreStore) ListUsersForDailyDigest(ctx context.Context) ([]*models.User, error) {
	iter := s.client.Collection("users").Where("preferences.daily_digest_enabled", "==", true).Documents(ctx)
	var users []*models.User
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		var u models.User
		if err := doc.DataTo(&u); err == nil {
			users = append(users, &u)
		}
	}
	return users, nil
}

// --- Package Operations ---

func (s *FirestoreStore) GetPackage(ctx context.Context, userID, packageID string) (*models.Package, error) {
	docRef := s.client.Collection("users").Doc(userID).Collection("packages").Doc(packageID)
	snap, err := docRef.Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var p models.Package
	if err := snap.DataTo(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *FirestoreStore) ListPackages(ctx context.Context, userID string, filterStatus models.PackageStatus) ([]*models.Package, error) {
	query := s.client.Collection("users").Doc(userID).Collection("packages").OrderBy("updated_at", firestore.Desc)
	if filterStatus != "" {
		query = query.Where("status", "==", string(filterStatus))
	}

	iter := query.Documents(ctx)
	var packages []*models.Package
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		var p models.Package
		if err := doc.DataTo(&p); err == nil {
			packages = append(packages, &p)
		}
	}
	return packages, nil
}

func (s *FirestoreStore) SavePackage(ctx context.Context, pkg *models.Package) error {
	docRef := s.client.Collection("users").Doc(pkg.UserID).Collection("packages").Doc(pkg.ID)
	_, err := docRef.Set(ctx, pkg)
	return err
}

func (s *FirestoreStore) UpdatePackage(ctx context.Context, pkg *models.Package) error {
	docRef := s.client.Collection("users").Doc(pkg.UserID).Collection("packages").Doc(pkg.ID)
	_, err := docRef.Set(ctx, pkg)
	return err
}

func (s *FirestoreStore) DeletePackage(ctx context.Context, userID, packageID string) error {
	docRef := s.client.Collection("users").Doc(userID).Collection("packages").Doc(packageID)
	_, err := docRef.Delete(ctx)
	return err
}

func (s *FirestoreStore) FindPackageByTrackingOrSender(ctx context.Context, userID, trackingNumber, sender string) (*models.Package, error) {
	col := s.client.Collection("users").Doc(userID).Collection("packages")

	// 1. Check exact match by tracking number if present
	cleanTracking := strings.TrimSpace(trackingNumber)
	if cleanTracking != "" {
		iter := col.Where("tracking_number", "==", cleanTracking).Limit(1).Documents(ctx)
		doc, err := iter.Next()
		if err == nil {
			var p models.Package
			if err := doc.DataTo(&p); err == nil {
				return &p, nil
			}
		}
		// If a tracking number was provided and not found, treat as new package (do not match other packages by sender)
		return nil, ErrNotFound
	}

	// 2. Check match by Sender where package is not delivered (only if no tracking number was specified)
	if strings.TrimSpace(sender) != "" {
		iter := col.Where("sender", "==", strings.TrimSpace(sender)).
			Where("status", "!=", string(models.StatusDelivered)).
			OrderBy("status", firestore.Asc).
			OrderBy("updated_at", firestore.Desc).
			Limit(1).
			Documents(ctx)
		doc, err := iter.Next()
		if err == nil {
			var p models.Package
			if err := doc.DataTo(&p); err == nil {
				return &p, nil
			}
		}
	}

	return nil, ErrNotFound
}

func (s *FirestoreStore) ListPackagesArrivingToday(ctx context.Context, userID, dateStr string) ([]*models.Package, error) {
	iter := s.client.Collection("users").Doc(userID).Collection("packages").
		Where("expected_delivery_date", "==", dateStr).
		Documents(ctx)

	var packages []*models.Package
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		var p models.Package
		if err := doc.DataTo(&p); err == nil {
			packages = append(packages, &p)
		}
	}
	return packages, nil
}


