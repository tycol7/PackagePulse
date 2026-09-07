package main

import (
	"context"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/tylerdean/package-tracker-demo/internal/agent"
	"github.com/tylerdean/package-tracker-demo/internal/auth"
	"github.com/tylerdean/package-tracker-demo/internal/config"
	"github.com/tylerdean/package-tracker-demo/internal/db"
	"github.com/tylerdean/package-tracker-demo/internal/handlers"
	"github.com/tylerdean/package-tracker-demo/internal/security"
	"github.com/tylerdean/package-tracker-demo/internal/storage"
	"github.com/tylerdean/package-tracker-demo/internal/telemetry"
	"github.com/tylerdean/package-tracker-demo/internal/templates"
	"github.com/tylerdean/package-tracker-demo/static"
)

func main() {
	ctx := context.Background()
	cfg := config.Load(ctx)

	log.Printf("==================================================================")
	log.Printf("🚀 Starting PackagePulse (Google Cloud AI Agent)")
	log.Printf("📦 Port: %s | Project ID: '%s' | Region: %s", cfg.Port, cfg.ProjectID, cfg.Region)
	log.Printf("==================================================================")

	// 0. Initialize Cloud Trace & Telemetry
	shutdownTracer, err := telemetry.InitTracer(ctx, cfg.ProjectID)
	if err != nil {
		log.Printf("⚠️  Warning: Could not initialize Cloud Trace exporter: %v", err)
	} else if shutdownTracer != nil {
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = shutdownTracer(shutdownCtx)
		}()
	}

	// 1. Initialize Store (Firestore Native or Memory fallback)
	var store db.Store
	if cfg.ProjectID != "" && !cfg.UseMockGCP {
		fsStore, err := db.NewFirestoreStore(ctx, cfg.ProjectID, cfg.FirestoreDatabaseID)
		if err != nil {
			log.Printf("⚠️  Warning: Could not connect to Cloud Firestore (%v). Falling back to in-memory store for local execution.", err)
			store = db.NewMemoryStore()
		} else {
			log.Printf("✅ Connected to Google Cloud Firestore database: %s", cfg.FirestoreDatabaseID)
			store = fsStore
		}
	} else {
		log.Printf("ℹ️  Running with in-memory store (Project ID not set or mock enabled)")
		store = db.NewMemoryStore()
	}
	defer store.Close()

	// 2. Initialize Blob Storage (GCS or Memory fallback)
	var blobStore storage.BlobStorage
	if cfg.GCSBucketName != "" && !cfg.UseMockGCP {
		gcs, err := storage.NewGCSStorage(ctx, cfg.GCSBucketName)
		if err != nil {
			log.Printf("⚠️  Warning: Could not connect to GCS bucket '%s' (%v). Falling back to memory storage.", cfg.GCSBucketName, err)
			blobStore = storage.NewMemoryStorage()
		} else {
			log.Printf("✅ Connected to Google Cloud Storage bucket: gs://%s", cfg.GCSBucketName)
			blobStore = gcs
		}
	} else {
		log.Printf("ℹ️  Running with in-memory blob storage for raw email bytes")
		blobStore = storage.NewMemoryStorage()
	}
	defer blobStore.Close()

	// 3. Initialize AI Agent (Agent Platform Gemini 2.5 Flash or Mock fallback)
	var aiAgent agent.LogisticsAgent
	if cfg.ProjectID != "" && !cfg.UseMockGCP {
		apAgent, err := agent.NewAgentPlatformAgent(ctx, cfg.ProjectID, cfg.Region, cfg.GeminiModel)
		if err != nil {
			log.Printf("⚠️  Warning: Could not connect to Agent Platform (%v). Falling back to local Mock Agent.", err)
			aiAgent = agent.NewMockAgent()
		} else {
			log.Printf("✅ Connected to Agent Platform Gemini model: %s", cfg.GeminiModel)
			aiAgent = apAgent
		}
	} else {
		log.Printf("ℹ️  Running with Mock Logistics Agent")
		aiAgent = agent.NewMockAgent()
	}
	defer aiAgent.Close()

	// 3b. Initialize Google Cloud Model Armor Security Service
	var modelArmor security.ModelArmorService
	if cfg.ProjectID != "" && !cfg.UseMockGCP {
		maClient, err := security.NewCloudModelArmorService(ctx, cfg.ProjectID, cfg.Region, cfg.ModelArmorTemplate)
		if err != nil {
			log.Printf("⚠️  Warning: Could not connect to Model Armor (%v). Falling back to mock model armor.", err)
			modelArmor = security.NewMockModelArmorService()
		} else {
			log.Printf("🛡️  Connected to Google Cloud Model Armor template: %s", cfg.ModelArmorTemplate)
			modelArmor = maClient
		}
	} else {
		log.Printf("ℹ️  Running with Mock Model Armor service")
		modelArmor = security.NewMockModelArmorService()
	}
	defer modelArmor.Close()

	// 4. Initialize Auth Service
	redirectURL := cfg.RedirectURL
	if redirectURL == "" {
		redirectURL = "http://localhost:" + cfg.Port + "/auth/callback"
	}
	authService := auth.NewService(cfg.GoogleClientID, cfg.GoogleClientSecret, redirectURL, cfg.SessionSecret)
	authMiddleware := auth.NewMiddleware(authService)

	// 5. Parse Templates
	tmpl, err := template.ParseFS(templates.FS, "*.html")
	if err != nil {
		log.Fatalf("Failed parsing templates: %v", err)
	}

	// 6. Initialize Handlers
	webHandler := handlers.NewWebHandler(tmpl, store, authService)
	pkgHandler := handlers.NewPackageHandler(tmpl, store)
	uploadHandler := handlers.NewUploadHandler(tmpl, store, blobStore, aiAgent, modelArmor)
	digestHandler := handlers.NewDigestHandler(tmpl, store, aiAgent)
	reHandler := handlers.NewReasoningEngineHandler(aiAgent, modelArmor)

	// 7. Route Registration
	mux := http.NewServeMux()

	// Static Assets
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(static.FS))))

	// Public Auth Endpoints
	mux.HandleFunc("/login", webHandler.ShowLogin)
	mux.HandleFunc("/auth/google", webHandler.StartGoogleOAuth)
	mux.HandleFunc("/auth/callback", webHandler.HandleGoogleCallback)
	mux.HandleFunc("/logout", webHandler.HandleLogout)

	// Health Check for Cloud Run & Agent Runtime
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// Google Cloud Agent Platform / Agent Runtime Contract Endpoints
	mux.HandleFunc("/api/reasoning_engine", reHandler.HandleQuery)
	mux.HandleFunc("/api/stream_reasoning_engine", reHandler.HandleStreamQuery)

	// Protected Routes (Require @google.com session)
	protectedMux := http.NewServeMux()
	protectedMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			webHandler.ShowDashboard(w, r)
			return
		}
		// Dispatch /packages routing
		if strings.HasPrefix(r.URL.Path, "/packages") {
			switch {
			case r.URL.Path == "/packages":
				if r.Method == http.MethodPost {
					pkgHandler.CreatePackage(w, r)
				} else {
					pkgHandler.ListPackages(w, r)
				}
			case r.URL.Path == "/packages/new":
				_ = tmpl.ExecuteTemplate(w, "package_form_modal", map[string]interface{}{})
			case strings.HasSuffix(r.URL.Path, "/edit"):
				pkgHandler.GetEditModal(w, r)
			case strings.HasSuffix(r.URL.Path, "/status"):
				pkgHandler.CycleStatus(w, r)
			default:
				if r.Method == http.MethodDelete {
					pkgHandler.DeletePackage(w, r)
				} else if r.Method == http.MethodPut {
					pkgHandler.UpdatePackage(w, r)
				}
			}
			return
		}
		http.NotFound(w, r)
	})

	protectedMux.HandleFunc("/settings", webHandler.SaveSettings)
	protectedMux.HandleFunc("/api/emails/upload", uploadHandler.HandleUpload)
	protectedMux.HandleFunc("/api/digest/modal", digestHandler.ShowModal)
	protectedMux.HandleFunc("/api/digest/generate", digestHandler.GeneratePreview)
	protectedMux.HandleFunc("/api/digest/preview", digestHandler.PreviewDigest)

	// Wrap protected routes with authentication middleware
	mux.Handle("/", authMiddleware.RequireAuth(protectedMux))

	// 8. Server Lifecycle & Graceful Shutdown
	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	go func() {
		log.Printf("🌐 Web server listening on http://localhost:%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Printf("🛑 Shutting down server gracefully...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("Error during shutdown: %v", err)
	}
	log.Printf("👋 Server stopped")
}
