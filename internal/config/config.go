package config

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strings"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
)

// Config holds runtime configuration loaded from environment variables and Google Secret Manager.
type Config struct {
	Port                string
	ProjectID           string
	Region              string
	GCSBucketName       string
	FirestoreDatabaseID string
	GoogleClientID      string
	GoogleClientSecret  string
	RedirectURL         string
	SessionSecret       string
	GeminiModel         string
	ModelArmorTemplate  string
	UseMockGCP          bool
}

// Load populates Config from the execution environment.
func Load(ctx context.Context) *Config {
	projectID := getEnv("PROJECT_ID", "")
	if projectID == "" {
		projectID = getEnv("GOOGLE_CLOUD_PROJECT", "")
	}
	if projectID == "" {
		projectID = getEnv("GCP_PROJECT", "")
	}
	if projectID == "" {
		projectID = "package-tracker-demo"
	}

	sessionSecret := getEnv("SESSION_SECRET", "")
	if sessionSecret == "" {
		if !strings.EqualFold(getEnv("USE_MOCK_GCP", "false"), "true") && os.Getenv("K_SERVICE") != "" {
			b := make([]byte, 32)
			if _, err := rand.Read(b); err == nil {
				sessionSecret = hex.EncodeToString(b)
				log.Printf("[Config] Generated ephemeral 256-bit session secret for Cloud Run instance")
			}
		}
		if sessionSecret == "" {
			sessionSecret = "super-secret-package-tracker-session-key-32b"
		}
	}

	cfg := &Config{
		Port:                getEnv("PORT", "8080"),
		ProjectID:           projectID,
		Region:              getEnv("REGION", "us-central1"),
		GCSBucketName:       getEnv("GCS_BUCKET_NAME", ""),
		FirestoreDatabaseID: getEnv("FIRESTORE_DATABASE_ID", "(default)"),
		GoogleClientID:      getEnv("GOOGLE_CLIENT_ID", ""),
		GoogleClientSecret:  getEnv("GOOGLE_CLIENT_SECRET", ""),
		RedirectURL:         getEnv("OAUTH_REDIRECT_URL", ""),
		SessionSecret:       sessionSecret,
		GeminiModel:         getEnv("GEMINI_MODEL", "gemini-2.5-flash"),
		ModelArmorTemplate:  getEnv("MODEL_ARMOR_TEMPLATE", "email-armor-guard"),
		UseMockGCP:          strings.EqualFold(getEnv("USE_MOCK_GCP", "false"), "true"),
	}

	// Try fetching client secret from Secret Manager if Secret Name is provided and secret is empty
	secretName := os.Getenv("SECRET_NAME_OAUTH")
	if cfg.GoogleClientSecret == "" && secretName != "" && cfg.ProjectID != "" && !cfg.UseMockGCP {
		if secretVal, err := fetchSecret(ctx, cfg.ProjectID, secretName); err == nil && secretVal != "" {
			cfg.GoogleClientSecret = secretVal
		} else if err != nil {
			log.Printf("[Config] Notice: Could not read secret %s from Secret Manager: %v", secretName, err)
		}
	}

	return cfg
}

func getEnv(key, defaultVal string) string {
	if val, ok := os.LookupEnv(key); ok && strings.TrimSpace(val) != "" {
		return strings.TrimSpace(val)
	}
	return defaultVal
}

func fetchSecret(ctx context.Context, projectID, secretName string) (string, error) {
	client, err := secretmanager.NewClient(ctx)
	if err != nil {
		return "", err
	}
	defer client.Close()

	req := &secretmanagerpb.AccessSecretVersionRequest{
		Name: fmt.Sprintf("projects/%s/secrets/%s/versions/latest", projectID, secretName),
	}
	result, err := client.AccessSecretVersion(ctx, req)
	if err != nil {
		return "", err
	}
	return string(result.Payload.Data), nil
}
