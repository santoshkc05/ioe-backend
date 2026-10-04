package config_test

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/platform/config"
)

func validEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL":       "postgres://ioe:ioe@localhost:5432/ioe",
		"GOOGLE_CLIENT_IDS":  "web.apps.googleusercontent.com, admin.apps.googleusercontent.com",
		"JWT_ISSUER":         "https://api.example.com",
		"JWT_AUDIENCE":       "ioe",
		"JWT_SIGNING_KEY":    "pem",
		"JWT_SIGNING_KEY_ID": "k1",
		"ALLOWED_ORIGINS":    "https://app.example.com, http://localhost:5173",
	}
}

func TestLoadDefaultsAndTrimming(t *testing.T) {
	cfg, err := config.LoadFrom(validEnv())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != ":8080" || !cfg.CookieSecure || cfg.AuthRateLimitPerMinute != 30 || cfg.LogLevel != "info" {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
	if cfg.GoogleJWKSURL != "https://www.googleapis.com/oauth2/v3/certs" {
		t.Fatalf("GoogleJWKSURL = %q", cfg.GoogleJWKSURL)
	}
	if got := cfg.GoogleClientIDs[1]; got != "admin.apps.googleusercontent.com" {
		t.Fatalf("list entry not trimmed: %q", got)
	}
	if got := cfg.AllowedOrigins[1]; got != "http://localhost:5173" {
		t.Fatalf("origin not trimmed: %q", got)
	}
	if len(cfg.BootstrapRootAdminEmails) != 0 {
		t.Fatalf("BootstrapRootAdminEmails = %q", cfg.BootstrapRootAdminEmails)
	}
	if lvl, err := cfg.SlogLevel(); err != nil || lvl != slog.LevelInfo {
		t.Fatalf("SlogLevel = %v, %v", lvl, err)
	}
}

func TestLoadNamesMissingVariable(t *testing.T) {
	env := validEnv()
	delete(env, "DATABASE_URL")
	_, err := config.LoadFrom(env)
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("err = %v, want mention of DATABASE_URL", err)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	cases := map[string]struct{ key, value string }{
		"origin with path":   {"ALLOWED_ORIGINS", "https://app.example.com/login"},
		"origin no scheme":   {"ALLOWED_ORIGINS", "app.example.com"},
		"bad cidr":           {"TRUSTED_PROXY_CIDRS", "10.0.0.0/33"},
		"zero rate":          {"AUTH_RATE_LIMIT_PER_MINUTE", "0"},
		"bad log level":      {"LOG_LEVEL", "loud"},
		"bad cookie boolean": {"COOKIE_SECURE", "maybe"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env := validEnv()
			env[tc.key] = tc.value
			_, err := config.LoadFrom(env)
			if err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("err = %v, want mention of %s", err, tc.key)
			}
		})
	}
}

func TestTrustedProxies(t *testing.T) {
	env := validEnv()
	env["TRUSTED_PROXY_CIDRS"] = "10.0.0.0/8, 192.168.1.7/32"
	cfg, err := config.LoadFrom(env)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.TrustedProxies(); len(got) != 2 || got[0].String() != "10.0.0.0/8" {
		t.Fatalf("TrustedProxies = %v", got)
	}
}
