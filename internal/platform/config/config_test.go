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

func TestSnowflakeNodeID(t *testing.T) {
	cfg, err := config.LoadFrom(validEnv())
	if err != nil || cfg.SnowflakeNodeID != 0 {
		t.Fatalf("default = %d, %v", cfg.SnowflakeNodeID, err)
	}
	for _, v := range []string{"-1", "1024", "x"} {
		env := validEnv()
		env["SNOWFLAKE_NODE_ID"] = v
		if _, err := config.LoadFrom(env); err == nil || !strings.Contains(err.Error(), "SNOWFLAKE_NODE_ID") {
			t.Fatalf("SNOWFLAKE_NODE_ID=%s: err = %v", v, err)
		}
	}
	env := validEnv()
	env["SNOWFLAKE_NODE_ID"] = "1023"
	if cfg, err := config.LoadFrom(env); err != nil || cfg.SnowflakeNodeID != 1023 {
		t.Fatalf("1023 = %d, %v", cfg.SnowflakeNodeID, err)
	}
}

const localNotificationKey = "c3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3Nzc3M"

func TestNotificationsDisabledByDefault(t *testing.T) {
	cfg, err := config.LoadFrom(validEnv())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NotificationsEnabled() {
		t.Fatal("notifications enabled without configuration")
	}
}

func TestNotificationsEnabled(t *testing.T) {
	env := validEnv()
	env["NOTIFICATION_SERVICE_BASE_URL"] = "http://notification:8080"
	env["NOTIFICATION_SERVICE_SEND_API_KEY"] = localNotificationKey
	cfg, err := config.LoadFrom(env)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.NotificationsEnabled() || cfg.NotificationServiceBaseURL != "http://notification:8080" || cfg.NotificationServiceSendAPIKey != localNotificationKey {
		t.Fatalf("%+v", cfg)
	}
}

func TestNotificationConfigRejectsInvalidValues(t *testing.T) {
	cases := map[string]struct{ url, key, wantVar string }{
		"url without key":       {"http://notification:8080", "", "NOTIFICATION_SERVICE_SEND_API_KEY"},
		"key without url":       {"", localNotificationKey, "NOTIFICATION_SERVICE_BASE_URL"},
		"url without scheme":    {"notification:8080", localNotificationKey, "NOTIFICATION_SERVICE_BASE_URL"},
		"url with ftp scheme":   {"ftp://notification", localNotificationKey, "NOTIFICATION_SERVICE_BASE_URL"},
		"url with query":        {"http://notification:8080?x=1", localNotificationKey, "NOTIFICATION_SERVICE_BASE_URL"},
		"short key":             {"http://notification:8080", "c2hvcnQ", "NOTIFICATION_SERVICE_SEND_API_KEY"},
		"padded key":            {"http://notification:8080", localNotificationKey + "=", "NOTIFICATION_SERVICE_SEND_API_KEY"},
		"standard alphabet key": {"http://notification:8080", "+" + localNotificationKey[1:], "NOTIFICATION_SERVICE_SEND_API_KEY"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env := validEnv()
			if tc.url != "" {
				env["NOTIFICATION_SERVICE_BASE_URL"] = tc.url
			}
			if tc.key != "" {
				env["NOTIFICATION_SERVICE_SEND_API_KEY"] = tc.key
			}
			_, err := config.LoadFrom(env)
			if err == nil || !strings.Contains(err.Error(), tc.wantVar) {
				t.Fatalf("err = %v, want mention of %s", err, tc.wantVar)
			}
			if tc.key != "" && strings.Contains(err.Error(), tc.key) {
				t.Fatalf("error echoes the key: %v", err)
			}
		})
	}
}
