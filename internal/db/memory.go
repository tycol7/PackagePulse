package db

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/tylerdean/package-tracker-demo/internal/models"
)

// MemoryStore provides a thread-safe in-memory implementation of Store for testing and local dev.
type MemoryStore struct {
	mu       sync.RWMutex
	users    map[string]*models.User
	packages map[string]map[string]*models.Package // userID -> packageID -> Package
}

// NewMemoryStore initializes a new MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		users:    make(map[string]*models.User),
		packages: make(map[string]map[string]*models.Package),
	}
}

func (m *MemoryStore) Close() error {
	return nil
}

func (m *MemoryStore) GetUser(ctx context.Context, id string) (*models.User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	u, ok := m.users[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *u
	return &cp, nil
}

func (m *MemoryStore) SaveUser(ctx context.Context, user *models.User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *user
	m.users[user.ID] = &cp
	return nil
}

func (m *MemoryStore) ListUsersForDailyDigest(ctx context.Context) ([]*models.User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var res []*models.User
	for _, u := range m.users {
		if u.Preferences.DailyDigestEnabled {
			cp := *u
			res = append(res, &cp)
		}
	}
	return res, nil
}

func (m *MemoryStore) GetPackage(ctx context.Context, userID, packageID string) (*models.Package, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	userPkgs, ok := m.packages[userID]
	if !ok {
		return nil, ErrNotFound
	}
	p, ok := userPkgs[packageID]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *p
	return &cp, nil
}

func (m *MemoryStore) ListPackages(ctx context.Context, userID string, filterStatus models.PackageStatus) ([]*models.Package, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	userPkgs, ok := m.packages[userID]
	if !ok {
		return []*models.Package{}, nil
	}

	var res []*models.Package
	for _, p := range userPkgs {
		if filterStatus == "" || p.Status == filterStatus {
			cp := *p
			res = append(res, &cp)
		}
	}

	sort.Slice(res, func(i, j int) bool {
		return res[i].UpdatedAt.After(res[j].UpdatedAt)
	})
	return res, nil
}

func (m *MemoryStore) SavePackage(ctx context.Context, pkg *models.Package) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.packages[pkg.UserID]; !ok {
		m.packages[pkg.UserID] = make(map[string]*models.Package)
	}
	cp := *pkg
	m.packages[pkg.UserID][pkg.ID] = &cp
	return nil
}

func (m *MemoryStore) UpdatePackage(ctx context.Context, pkg *models.Package) error {
	return m.SavePackage(ctx, pkg)
}

func (m *MemoryStore) DeletePackage(ctx context.Context, userID, packageID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if userPkgs, ok := m.packages[userID]; ok {
		delete(userPkgs, packageID)
	}
	return nil
}

func (m *MemoryStore) FindPackageByTrackingOrSender(ctx context.Context, userID, trackingNumber, sender string) (*models.Package, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	userPkgs, ok := m.packages[userID]
	if !ok {
		return nil, ErrNotFound
	}

	cleanTracking := strings.TrimSpace(trackingNumber)
	cleanSender := strings.ToLower(strings.TrimSpace(sender))

	// 1. Search by tracking number
	if cleanTracking != "" {
		for _, p := range userPkgs {
			if strings.EqualFold(strings.TrimSpace(p.TrackingNumber), cleanTracking) {
				cp := *p
				return &cp, nil
			}
		}
		// If a tracking number was specified and not found, this is a distinct new package.
		return nil, ErrNotFound
	}

	// 2. Search by sender if not delivered (only if no tracking number was specified)
	if cleanSender != "" {
		var candidate *models.Package
		for _, p := range userPkgs {
			if strings.Contains(strings.ToLower(p.Sender), cleanSender) && p.Status != models.StatusDelivered {
				if candidate == nil || p.UpdatedAt.After(candidate.UpdatedAt) {
					cp := *p
					candidate = &cp
				}
			}
		}
		if candidate != nil {
			return candidate, nil
		}
	}

	return nil, ErrNotFound
}

func (m *MemoryStore) ListPackagesArrivingToday(ctx context.Context, userID, dateStr string) ([]*models.Package, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	userPkgs, ok := m.packages[userID]
	if !ok {
		return []*models.Package{}, nil
	}

	var res []*models.Package
	for _, p := range userPkgs {
		if p.ExpectedDeliveryDate == dateStr {
			cp := *p
			res = append(res, &cp)
		}
	}
	return res, nil
}


