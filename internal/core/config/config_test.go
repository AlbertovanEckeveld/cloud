package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validEnv returns a minimal valid development configuration.
func validEnv() map[string]string {
	return map[string]string{
		"APP_ENV":              "development",
		"SITE_ID":              "home",
		"DATABASE_URL":         "postgres://user:hunter2@db.internal:5432/app",
		"S3_ENDPOINT":          "http://s3.internal:9000",
		"S3_BUCKET":            "platform",
		"S3_ACCESS_KEY_ID":     "access",
		"S3_SECRET_ACCESS_KEY": "s3cr3t",
		"PUBLIC_URL":           "http://127.0.0.1:8080",
		"ENCRYPTION_KEY":       "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=",
	}
}

func getenv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadValid(t *testing.T) {
	cfg, err := Load(getenv(validEnv()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Env != EnvDevelopment || cfg.SiteID != "home" {
		t.Errorf("unexpected env/site: %q %q", cfg.Env, cfg.SiteID)
	}
	if cfg.HTTPAddr != "127.0.0.1:8080" {
		t.Errorf("HTTPAddr default = %q, want loopback only", cfg.HTTPAddr)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v", cfg.LogLevel)
	}
	if cfg.Storage.Endpoint != "s3.internal:9000" || cfg.Storage.UseTLS {
		t.Errorf("storage endpoint = %q tls=%v", cfg.Storage.Endpoint, cfg.Storage.UseTLS)
	}
	if cfg.Storage.Region != "us-east-1" {
		t.Errorf("Region default = %q", cfg.Storage.Region)
	}
	if cfg.Storage.SecretAccessKey.Reveal() != "s3cr3t" {
		t.Error("secret not loaded")
	}
	if cfg.PublicURL.String() != "http://127.0.0.1:8080" {
		t.Errorf("PublicURL = %q", cfg.PublicURL)
	}
	if key := cfg.EncryptionKey.Reveal(); len(key) != 32 || key[1] != 1 || key[31] != 31 {
		t.Errorf("EncryptionKey not decoded to 32 raw bytes")
	}
}

func TestLoadDefaultsToProduction(t *testing.T) {
	env := validEnv()
	delete(env, "APP_ENV")
	_, err := Load(getenv(env))
	// The development fixture uses plain HTTP and no TLS, which production rejects.
	if err == nil {
		t.Fatal("expected production rules to reject insecure transports")
	}
	for _, want := range []string{"sslmode=verify-full", "S3_ENDPOINT must use https", "PUBLIC_URL must use https"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestLoadProductionValid(t *testing.T) {
	env := validEnv()
	env["APP_ENV"] = "production"
	env["DATABASE_URL"] = "postgres://user:pw@db.internal/app?sslmode=verify-full"
	env["S3_ENDPOINT"] = "https://s3.internal"
	env["PUBLIC_URL"] = "https://cloud.example.com/"
	cfg, err := Load(getenv(env))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Storage.UseTLS || cfg.Storage.Endpoint != "s3.internal" {
		t.Errorf("endpoint = %q tls=%v", cfg.Storage.Endpoint, cfg.Storage.UseTLS)
	}
}

func TestLoadReportsAllErrorsAtOnce(t *testing.T) {
	_, err := Load(getenv(map[string]string{}))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"SITE_ID", "PUBLIC_URL", "ENCRYPTION_KEY", "DATABASE_URL", "S3_ENDPOINT", "S3_BUCKET", "S3_ACCESS_KEY_ID", "S3_SECRET_ACCESS_KEY"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s:\n%v", want, err)
		}
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		key, value, wantErr string
	}{
		{"APP_ENV", "staging", "APP_ENV"},
		{"SITE_ID", "Home", "SITE_ID"},
		{"SITE_ID", "-home", "SITE_ID"},
		{"SITE_ID", strings.Repeat("a", 33), "SITE_ID"},
		{"HTTP_ADDR", "8080", "HTTP_ADDR"},
		{"LOG_LEVEL", "verbose", "LOG_LEVEL"},
		{"S3_FORCE_PATH_STYLE", "maybe", "S3_FORCE_PATH_STYLE"},
		{"S3_BUCKET", "Bad_Bucket", "S3_BUCKET"},
		{"S3_ENDPOINT", "ftp://s3.internal", "S3_ENDPOINT"},
		{"S3_ENDPOINT", "s3.internal:9000", "S3_ENDPOINT"},
		{"S3_ENDPOINT", "https://user:pw@s3.internal", "S3_ENDPOINT"},
		{"S3_ENDPOINT", "https://s3.internal/bucket", "S3_ENDPOINT"},
		{"DATABASE_URL", "mysql://db/app", "DATABASE_URL"},
		{"DATABASE_URL", "postgres:///app", "DATABASE_URL"},
		{"PUBLIC_URL", "cloud.example.com", "PUBLIC_URL"},
		{"PUBLIC_URL", "https://cloud.example.com/app", "PUBLIC_URL"},
		{"PUBLIC_URL", "javascript://x", "PUBLIC_URL"},
		{"ENCRYPTION_KEY", "too-short", "ENCRYPTION_KEY"},
		{"ENCRYPTION_KEY", "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwd", "ENCRYPTION_KEY"},
	}
	for _, tt := range tests {
		t.Run(tt.key+"="+tt.value, func(t *testing.T) {
			env := validEnv()
			env[tt.key] = tt.value
			_, err := Load(getenv(env))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want mention of %s", err, tt.wantErr)
			}
		})
	}
}

func TestLoadNeverLeaksDatabasePassword(t *testing.T) {
	env := validEnv()
	// Invalid URL (bad escape) that contains a password.
	env["DATABASE_URL"] = "postgres://user:hunter2%zz@db/app"
	_, err := Load(getenv(env))
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("error leaks password: %v", err)
	}
}

func TestSecretFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s3_secret")
	if err := os.WriteFile(path, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := validEnv()
	delete(env, "S3_SECRET_ACCESS_KEY")
	env["S3_SECRET_ACCESS_KEY_FILE"] = path

	cfg, err := Load(getenv(env))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Storage.SecretAccessKey.Reveal(); got != "from-file" {
		t.Errorf("secret = %q, want trailing newline trimmed", got)
	}
}

func TestSecretValueAndFileIsAmbiguous(t *testing.T) {
	env := validEnv()
	env["S3_SECRET_ACCESS_KEY_FILE"] = "/run/secrets/s3"
	_, err := Load(getenv(env))
	if err == nil || !strings.Contains(err.Error(), "only one of") {
		t.Fatalf("err = %v", err)
	}
}

func TestSecretFileMissing(t *testing.T) {
	env := validEnv()
	delete(env, "S3_SECRET_ACCESS_KEY")
	env["S3_SECRET_ACCESS_KEY_FILE"] = filepath.Join(t.TempDir(), "missing")
	if _, err := Load(getenv(env)); err == nil {
		t.Fatal("expected error for missing secret file")
	}
}

func TestSecretIsRedactedEverywhere(t *testing.T) {
	s := Secret("hunter2")
	var logged strings.Builder
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("x", "secret", s)
	j, _ := json.Marshal(struct{ S Secret }{s})

	outputs := map[string]string{
		"%s":   fmt.Sprintf("%s", s),
		"%v":   fmt.Sprintf("%v", s),
		"%#v":  fmt.Sprintf("%#v", s),
		"%+v":  fmt.Sprintf("%+v", struct{ S Secret }{s}),
		"slog": logged.String(),
		"json": string(j),
	}
	for name, out := range outputs {
		if strings.Contains(out, "hunter2") {
			t.Errorf("%s leaks secret: %s", name, out)
		}
	}
	if s.Reveal() != "hunter2" {
		t.Error("Reveal must return the raw value")
	}
}
