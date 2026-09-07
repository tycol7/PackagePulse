package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/tylerdean/package-tracker-demo/internal/auth"
	"github.com/tylerdean/package-tracker-demo/internal/db"
	"github.com/tylerdean/package-tracker-demo/internal/models"
)

type PackageHandler struct {
	tmpl  *template.Template
	store db.Store
}

func NewPackageHandler(tmpl *template.Template, store db.Store) *PackageHandler {
	return &PackageHandler{
		tmpl:  tmpl,
		store: store,
	}
}

// ListPackages returns the filtered list of packages as an HTMX fragment.
func (h *PackageHandler) ListPackages(w http.ResponseWriter, r *http.Request) {
	session := auth.GetUserFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	filter := r.URL.Query().Get("status")
	var statusFilter models.PackageStatus
	if filter != "All" && filter != "" {
		statusFilter = models.PackageStatus(filter)
	}

	packages, err := h.store.ListPackages(r.Context(), session.UserID, statusFilter)
	if err != nil {
		http.Error(w, "Failed listing packages", http.StatusInternalServerError)
		return
	}

	data := map[string]interface{}{
		"Packages":     packages,
		"ActiveFilter": filter,
	}
	_ = h.tmpl.ExecuteTemplate(w, "package_list", data)

	// Keep status pill counts fresh
	allPackages, err := h.store.ListPackages(r.Context(), session.UserID, "")
	if err == nil {
		_ = h.tmpl.ExecuteTemplate(w, "status_pill_counts", models.CalculateStatusCounts(allPackages))
	}
}

// CreatePackage creates a package manually from the web UI.
func (h *PackageHandler) CreatePackage(w http.ResponseWriter, r *http.Request) {
	session := auth.GetUserFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}

	now := time.Now().UTC()
	pkg := &models.Package{
		ID:                   generatePackageID(),
		UserID:               session.UserID,
		Sender:               models.SanitizeCleanString(r.FormValue("sender")),
		Carrier:              models.SanitizeCleanString(r.FormValue("carrier")),
		TrackingNumber:       models.SanitizeCleanString(r.FormValue("tracking_number")),
		TrackingLink:         models.SanitizeCleanURL(r.FormValue("tracking_link")),
		ExpectedDeliveryDate: models.SanitizeCleanString(r.FormValue("expected_delivery_date")),
		Status:               models.PackageStatus(r.FormValue("status")),
		Notes:                models.SanitizeCleanString(r.FormValue("notes")),
		Source:               "MANUAL",
		CreatedAt:            now,
		UpdatedAt:            now,
	}

	if pkg.Status == "" {
		pkg.Status = models.StatusOrdered
	}

	if err := h.store.SavePackage(r.Context(), pkg); err != nil {
		http.Error(w, "Failed creating package: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Trigger toast & re-render full package list + updated status pill counts
	w.Header().Set("HX-Trigger", `{"showToast": "Package created successfully!"}`)
	packages, _ := h.store.ListPackages(r.Context(), session.UserID, "")
	_ = h.tmpl.ExecuteTemplate(w, "package_list", map[string]interface{}{
		"Packages": packages,
	})
	_ = h.tmpl.ExecuteTemplate(w, "status_pill_counts", models.CalculateStatusCounts(packages))
}

// GetEditModal returns the package form populated with the package details.
func (h *PackageHandler) GetEditModal(w http.ResponseWriter, r *http.Request) {
	session := auth.GetUserFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	id := extractIDFromPath(r.URL.Path, "/packages/", "/edit")
	pkg, err := h.store.GetPackage(r.Context(), session.UserID, id)
	if err != nil {
		http.Error(w, "Package not found", http.StatusNotFound)
		return
	}

	_ = h.tmpl.ExecuteTemplate(w, "package_form_modal", pkg)
}

// UpdatePackage updates a package manually.
func (h *PackageHandler) UpdatePackage(w http.ResponseWriter, r *http.Request) {
	session := auth.GetUserFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}

	id := extractIDFromPath(r.URL.Path, "/packages/", "")
	pkg, err := h.store.GetPackage(r.Context(), session.UserID, id)
	if err != nil {
		http.Error(w, "Package not found", http.StatusNotFound)
		return
	}

	pkg.Sender = models.SanitizeCleanString(r.FormValue("sender"))
	pkg.Carrier = models.SanitizeCleanString(r.FormValue("carrier"))
	pkg.TrackingNumber = models.SanitizeCleanString(r.FormValue("tracking_number"))
	pkg.TrackingLink = models.SanitizeCleanURL(r.FormValue("tracking_link"))
	pkg.ExpectedDeliveryDate = models.SanitizeCleanString(r.FormValue("expected_delivery_date"))
	pkg.Status = models.PackageStatus(r.FormValue("status"))
	pkg.Notes = models.SanitizeCleanString(r.FormValue("notes"))
	pkg.UpdatedAt = time.Now().UTC()

	if err := h.store.UpdatePackage(r.Context(), pkg); err != nil {
		http.Error(w, "Failed updating package", http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Trigger", `{"showToast": "Package updated successfully!"}`)
	_ = h.tmpl.ExecuteTemplate(w, "package_card", pkg)
	allPkgs, err := h.store.ListPackages(r.Context(), session.UserID, "")
	if err == nil {
		_ = h.tmpl.ExecuteTemplate(w, "status_pill_counts", models.CalculateStatusCounts(allPkgs))
	}
}

// DeletePackage deletes a package.
func (h *PackageHandler) DeletePackage(w http.ResponseWriter, r *http.Request) {
	session := auth.GetUserFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	id := extractIDFromPath(r.URL.Path, "/packages/", "")
	if err := h.store.DeletePackage(r.Context(), session.UserID, id); err != nil {
		http.Error(w, "Failed deleting package", http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Trigger", `{"showToast": "Package deleted"}`)
	allPkgs, err := h.store.ListPackages(r.Context(), session.UserID, "")
	if err == nil {
		if len(allPkgs) == 0 {
			_ = h.tmpl.ExecuteTemplate(w, "package_list_oob", map[string]interface{}{
				"Packages": allPkgs,
			})
		}
		_ = h.tmpl.ExecuteTemplate(w, "status_pill_counts", models.CalculateStatusCounts(allPkgs))
	}
	w.WriteHeader(http.StatusOK)
}

// CycleStatus advances a package to its next lifecycle status.
func (h *PackageHandler) CycleStatus(w http.ResponseWriter, r *http.Request) {
	session := auth.GetUserFromContext(r.Context())
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	id := extractIDFromPath(r.URL.Path, "/packages/", "/status")
	pkg, err := h.store.GetPackage(r.Context(), session.UserID, id)
	if err != nil {
		http.Error(w, "Package not found", http.StatusNotFound)
		return
	}

	// Advance status
	switch pkg.Status {
	case models.StatusOrdered:
		pkg.Status = models.StatusInTransit
	case models.StatusInTransit:
		pkg.Status = models.StatusOutForDelivery
	case models.StatusOutForDelivery:
		pkg.Status = models.StatusDelivered
	case models.StatusDelivered:
		pkg.Status = models.StatusOrdered
	case models.StatusException:
		pkg.Status = models.StatusInTransit
	default:
		pkg.Status = models.StatusInTransit
	}
	pkg.UpdatedAt = time.Now().UTC()

	if err := h.store.UpdatePackage(r.Context(), pkg); err != nil {
		http.Error(w, "Failed updating status", http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Trigger", `{"showToast": "Status changed to `+string(pkg.Status)+`"}`)
	_ = h.tmpl.ExecuteTemplate(w, "package_card", pkg)
	allPkgs, err := h.store.ListPackages(r.Context(), session.UserID, "")
	if err == nil {
		_ = h.tmpl.ExecuteTemplate(w, "status_pill_counts", models.CalculateStatusCounts(allPkgs))
	}
}

func extractIDFromPath(path, prefix, suffix string) string {
	s := strings.TrimPrefix(path, prefix)
	if suffix != "" {
		s = strings.TrimSuffix(s, suffix)
	}
	return strings.Trim(s, "/")
}

func generatePackageID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "pkg_" + hex.EncodeToString(b)
}
