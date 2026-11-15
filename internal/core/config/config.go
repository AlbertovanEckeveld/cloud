// Package config loads and validates the platform configuration from
// environment variables.
//
// Loading fails fast: every problem is collected and reported at once, and the
// platform refuses to start with an invalid or insecure configuration.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Env is the deployment environment.
type Env string

const (
	EnvDevelopment Env = "development"
	EnvProduction  Env = "production"
)

// Config is the complete, validated platform configuration.
type Config struct {
	Env      Env
	SiteID   string
	HTTPAddr string
	// PublicURL is the origin users reach the platform at, e.g. https://cloud.example.com.
	PublicURL *url.URL
	// EncryptionKey is the 32-byte key for encrypting secrets at rest.
	EncryptionKey Secret
	LogLevel      slog.Level
	Database      Database
	Storage       Storage
}

// Database configures the PostgreSQL connection.
type Database struct {
	// URL is a postgres:// connection URL. It contains the password.
	URL Secret
}

// Storage configures the S3-compatible object store.
type Storage struct {
	// Endpoint is host[:port], without scheme.
	Endpoint        string
	UseTLS          bool
	Region          string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey Secret
	PathStyle       bool
}

// siteIDPattern keeps site IDs short, lowercase and safe to use in object keys,
// log fields and metric labels.
var siteIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// bucketPattern follows the S3 bucket naming rules (lowercase, digits, dots, hyphens).
var bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

// Load reads the configuration using getenv (normally os.Getenv).
//
// Secrets can also be provided as a file: when NAME_FILE is set, the secret is
// read from that path (for Docker/Compose secrets). Setting both NAME and
// NAME_FILE is an error, because it is ambiguous which one wins.
func Load(getenv func(string) string) (Config, error) {
	l := loader{getenv: getenv}

	cfg := Config{
		Env:           Env(l.str("APP_ENV", string(EnvProduction))),
		SiteID:        l.required("SITE_ID"),
		HTTPAddr:      l.str("HTTP_ADDR", "127.0.0.1:8080"),
		LogLevel:      l.logLevel("LOG_LEVEL"),
		EncryptionKey: l.secret("ENCRYPTION_KEY"),
		Database: Database{
			URL: l.secret("DATABASE_URL"),
		},
		Storage: Storage{
			Region:          l.str("S3_REGION", "us-east-1"),
			Bucket:          l.required("S3_BUCKET"),
			AccessKeyID:     l.required("S3_ACCESS_KEY_ID"),
			SecretAccessKey: l.secret("S3_SECRET_ACCESS_KEY"),
			PathStyle:       l.boolean("S3_FORCE_PATH_STYLE", false),
		},
	}

	if cfg.Env != EnvDevelopment && cfg.Env != EnvProduction {
		l.fail("APP_ENV must be %q or %q", EnvDevelopment, EnvProduction)
	}
	if cfg.SiteID != "" && !siteIDPattern.MatchString(cfg.SiteID) {
		l.fail("SITE_ID must match %s", siteIDPattern)
	}
	if _, _, err := net.SplitHostPort(cfg.HTTPAddr); err != nil {
		l.fail("HTTP_ADDR must be host:port: %v", err)
	}
	if cfg.Storage.Bucket != "" && !bucketPattern.MatchString(cfg.Storage.Bucket) {
		l.fail("S3_BUCKET is not a valid bucket name")
	}

	l.validateDatabaseURL(cfg.Database.URL, cfg.Env)
	cfg.PublicURL = l.publicURL("PUBLIC_URL", cfg.Env)
	cfg.EncryptionKey = l.encryptionKey("ENCRYPTION_KEY", cfg.EncryptionKey)
	cfg.Storage.Endpoint, cfg.Storage.UseTLS = l.storageEndpoint("S3_ENDPOINT", cfg.Env)

	if err := errors.Join(l.errs...); err != nil {
		return Config{}, fmt.Errorf("invalid configuration:\n%w", err)
	}
	return cfg, nil
}

type loader struct {
	getenv func(string) string
	errs   []error
}

func (l *loader) fail(format string, args ...any) {
	l.errs = append(l.errs, fmt.Errorf(format, args...))
}

func (l *loader) str(name, fallback string) string {
	if v := strings.TrimSpace(l.getenv(name)); v != "" {
		return v
	}
	return fallback
}

func (l *loader) required(name string) string {
	v := l.str(name, "")
	if v == "" {
		l.fail("%s is required", name)
	}
	return v
}

func (l *loader) secret(name string) Secret {
	value := l.getenv(name)
	file := l.getenv(name + "_FILE")
	switch {
	case value != "" && file != "":
		l.fail("set only one of %s and %s_FILE", name, name)
		return ""
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			l.fail("%s_FILE: %v", name, err)
			return ""
		}
		value = strings.TrimRight(string(b), "\r\n")
	}
	if value == "" {
		l.fail("%s (or %s_FILE) is required", name, name)
	}
	return Secret(value)
}

func (l *loader) boolean(name string, fallback bool) bool {
	raw := l.str(name, "")
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		l.fail("%s must be true or false", name)
	}
	return v
}

func (l *loader) logLevel(name string) slog.Level {
	var level slog.Level
	if err := level.UnmarshalText([]byte(l.str(name, "info"))); err != nil {
		l.fail("%s must be debug, info, warn or error", name)
	}
	return level
}

// validateDatabaseURL checks the URL form and, in production, that the
// connection verifies the server certificate. sslmode=prefer or require would
// still allow a man-in-the-middle to impersonate the database.
func (l *loader) validateDatabaseURL(s Secret, env Env) {
	if s == "" {
		return
	}
	u, err := url.Parse(s.Reveal())
	if err != nil {
		// url.Error would echo the URL including the password; do not wrap it.
		l.fail("DATABASE_URL is not a valid URL")
		return
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		l.fail("DATABASE_URL must start with postgres:// or postgresql://")
		return
	}
	if u.Hostname() == "" {
		l.fail("DATABASE_URL must contain a host")
	}
	if env == EnvProduction && u.Query().Get("sslmode") != "verify-full" {
		l.fail("DATABASE_URL must use sslmode=verify-full in production")
	}
}

// publicURL requires a bare origin (scheme, host, optional port). In
// production it must be https, because session cookies are then Secure-only.
func (l *loader) publicURL(name string, env Env) *url.URL {
	raw := l.required(name)
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		l.fail("%s must be a URL like https://cloud.example.com", name)
		return nil
	}
	if u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		l.fail("%s must contain only scheme, host and port", name)
		return nil
	}
	if env == EnvProduction && u.Scheme != "https" {
		l.fail("%s must use https in production", name)
	}
	u.Path = ""
	return u
}

// encryptionKey decodes a base64 key and requires exactly 32 bytes (AES-256).
// Generate one with: openssl rand -base64 32
func (l *loader) encryptionKey(name string, s Secret) Secret {
	if s == "" {
		return ""
	}
	key, err := base64.StdEncoding.DecodeString(s.Reveal())
	if err != nil || len(key) != 32 {
		l.fail("%s must be 32 random bytes, base64-encoded (openssl rand -base64 32)", name)
		return ""
	}
	return Secret(key)
}

func (l *loader) storageEndpoint(name string, env Env) (hostport string, useTLS bool) {
	raw := l.required(name)
	if raw == "" {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		l.fail("%s must be a URL like https://s3.example.com", name)
		return "", false
	}
	if u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		l.fail("%s must contain only scheme, host and port", name)
	}
	switch u.Scheme {
	case "https":
		useTLS = true
	case "http":
		if env == EnvProduction {
			l.fail("%s must use https in production", name)
		}
	default:
		l.fail("%s must use http or https", name)
	}
	return u.Host, useTLS
}
