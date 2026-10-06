// Package config loads process configuration from the environment and fails fast on invalid values.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"os"
	"strings"

	"github.com/caarlos0/env/v11"
)

// Config is the complete API server configuration.
type Config struct {
	HTTPAddr                      string   `env:"HTTP_ADDR" envDefault:":8080"`
	DatabaseURL                   string   `env:"DATABASE_URL,required,notEmpty"`
	GoogleClientIDs               []string `env:"GOOGLE_CLIENT_IDS,required,notEmpty" envSeparator:","`
	GoogleJWKSURL                 string   `env:"GOOGLE_JWKS_URL" envDefault:"https://www.googleapis.com/oauth2/v3/certs"`
	JWTIssuer                     string   `env:"JWT_ISSUER,required,notEmpty"`
	JWTAudience                   string   `env:"JWT_AUDIENCE,required,notEmpty"`
	JWTSigningKeyPEM              string   `env:"JWT_SIGNING_KEY,required,notEmpty"`
	JWTSigningKeyID               string   `env:"JWT_SIGNING_KEY_ID,required,notEmpty"`
	JWTVerifyKeys                 string   `env:"JWT_VERIFY_KEYS"`
	AllowedOrigins                []string `env:"ALLOWED_ORIGINS,required,notEmpty" envSeparator:","`
	BootstrapRootAdminEmails      []string `env:"BOOTSTRAP_ROOT_ADMIN_EMAILS" envSeparator:","`
	CookieSecure                  bool     `env:"COOKIE_SECURE" envDefault:"true"`
	AuthRateLimitPerMinute        int      `env:"AUTH_RATE_LIMIT_PER_MINUTE" envDefault:"30"`
	SnowflakeNodeID               int64    `env:"SNOWFLAKE_NODE_ID" envDefault:"0"`
	TrustedProxyCIDRs             []string `env:"TRUSTED_PROXY_CIDRS" envSeparator:","`
	LogLevel                      string   `env:"LOG_LEVEL" envDefault:"info"`
	NotificationServiceBaseURL    string   `env:"NOTIFICATION_SERVICE_BASE_URL"`
	NotificationServiceSendAPIKey string   `env:"NOTIFICATION_SERVICE_SEND_API_KEY"`
	MediaServiceBaseURL           string   `env:"MEDIA_SERVICE_BASE_URL"`
	MediaServicePublicURL         string   `env:"MEDIA_SERVICE_PUBLIC_URL"`
	MediaServiceAPIKey            string   `env:"MEDIA_SERVICE_API_KEY"`
}

// Load reads the process environment.
func Load() (Config, error) {
	return LoadFrom(env.ToMap(os.Environ()))
}

// LoadFrom reads configuration from the given variables.
func LoadFrom(environ map[string]string) (Config, error) {
	cfg, err := env.ParseAsWithOptions[Config](env.Options{Environment: environ})
	if err != nil {
		return Config{}, withVarNames(err)
	}
	cfg.normalize()
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// LoadDatabaseURL reads only DATABASE_URL, for commands that need nothing else.
func LoadDatabaseURL() (string, error) {
	u := os.Getenv("DATABASE_URL")
	if u == "" {
		return "", errors.New("DATABASE_URL is required")
	}
	return u, nil
}

// SlogLevel parses LogLevel.
func (c Config) SlogLevel() (slog.Level, error) {
	var l slog.Level
	err := l.UnmarshalText([]byte(c.LogLevel))
	return l, err
}

// TrustedProxies returns the parsed TRUSTED_PROXY_CIDRS. Values were validated by LoadFrom.
func (c Config) TrustedProxies() []netip.Prefix {
	out := make([]netip.Prefix, 0, len(c.TrustedProxyCIDRs))
	for _, s := range c.TrustedProxyCIDRs {
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p.Masked())
		}
	}
	return out
}

// NotificationsEnabled reports whether the notification service is configured.
// LoadFrom guarantees that both variables are set or neither is.
func (c Config) NotificationsEnabled() bool { return c.NotificationServiceBaseURL != "" }

// MediaEnabled reports whether the media service is configured.
// LoadFrom guarantees that all three media variables are set or none is.
func (c Config) MediaEnabled() bool { return c.MediaServiceBaseURL != "" }

func (c *Config) normalize() {
	c.GoogleClientIDs = cleanList(c.GoogleClientIDs)
	c.AllowedOrigins = cleanList(c.AllowedOrigins)
	c.BootstrapRootAdminEmails = cleanList(c.BootstrapRootAdminEmails)
	c.TrustedProxyCIDRs = cleanList(c.TrustedProxyCIDRs)
}

func (c *Config) validate() error {
	var errs []error
	if len(c.GoogleClientIDs) == 0 {
		errs = append(errs, errors.New("GOOGLE_CLIENT_IDS: at least one client ID is required"))
	}
	if len(c.AllowedOrigins) == 0 {
		errs = append(errs, errors.New("ALLOWED_ORIGINS: at least one origin is required"))
	}
	for _, o := range c.AllowedOrigins {
		if !isOrigin(o) {
			errs = append(errs, fmt.Errorf("ALLOWED_ORIGINS: %q is not an origin (scheme://host[:port])", o))
		}
	}
	for _, s := range c.TrustedProxyCIDRs {
		if _, err := netip.ParsePrefix(s); err != nil {
			errs = append(errs, fmt.Errorf("TRUSTED_PROXY_CIDRS: %q: %w", s, err))
		}
	}
	if c.AuthRateLimitPerMinute <= 0 {
		errs = append(errs, errors.New("AUTH_RATE_LIMIT_PER_MINUTE: must be positive"))
	}
	if c.SnowflakeNodeID < 0 || c.SnowflakeNodeID > 1023 {
		errs = append(errs, errors.New("SNOWFLAKE_NODE_ID: must be between 0 and 1023"))
	}
	if _, err := c.SlogLevel(); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL: %w", err))
	}
	errs = append(errs, c.validateNotifications()...)
	errs = append(errs, c.validateMedia()...)
	return errors.Join(errs...)
}

func (c *Config) validateNotifications() []error {
	hasURL, hasKey := c.NotificationServiceBaseURL != "", c.NotificationServiceSendAPIKey != ""
	switch {
	case !hasURL && !hasKey:
		return nil
	case !hasKey:
		return []error{errors.New("NOTIFICATION_SERVICE_SEND_API_KEY: required when NOTIFICATION_SERVICE_BASE_URL is set")}
	case !hasURL:
		return []error{errors.New("NOTIFICATION_SERVICE_BASE_URL: required when NOTIFICATION_SERVICE_SEND_API_KEY is set")}
	}
	var errs []error
	if !isBaseURL(c.NotificationServiceBaseURL) {
		errs = append(errs, fmt.Errorf("NOTIFICATION_SERVICE_BASE_URL: %q is not an absolute http(s) URL without query or fragment", c.NotificationServiceBaseURL))
	}
	if key, err := base64.RawURLEncoding.DecodeString(c.NotificationServiceSendAPIKey); err != nil || len(key) < 32 {
		errs = append(errs, errors.New("NOTIFICATION_SERVICE_SEND_API_KEY: must be unpadded base64url encoding at least 32 bytes"))
	}
	return errs
}

const minMediaAPIKeyLen = 32

func (c *Config) validateMedia() []error {
	vars := []struct{ name, value string }{
		{"MEDIA_SERVICE_BASE_URL", c.MediaServiceBaseURL},
		{"MEDIA_SERVICE_PUBLIC_URL", c.MediaServicePublicURL},
		{"MEDIA_SERVICE_API_KEY", c.MediaServiceAPIKey},
	}
	var set, missing []string
	for _, v := range vars {
		if v.value == "" {
			missing = append(missing, v.name)
		} else {
			set = append(set, v.name)
		}
	}
	if len(set) == 0 {
		return nil
	}
	var errs []error
	for _, name := range missing {
		errs = append(errs, fmt.Errorf("%s: required when %s is set", name, strings.Join(set, " and ")))
	}
	if len(errs) > 0 {
		return errs
	}
	if !isBaseURL(c.MediaServiceBaseURL) {
		errs = append(errs, fmt.Errorf("MEDIA_SERVICE_BASE_URL: %q is not an absolute http(s) URL without query or fragment", c.MediaServiceBaseURL))
	}
	if !isBaseURL(c.MediaServicePublicURL) {
		errs = append(errs, fmt.Errorf("MEDIA_SERVICE_PUBLIC_URL: %q is not an absolute http(s) URL without query or fragment", c.MediaServicePublicURL))
	}
	if len(c.MediaServiceAPIKey) < minMediaAPIKeyLen {
		errs = append(errs, fmt.Errorf("MEDIA_SERVICE_API_KEY: must be at least %d characters", minMediaAPIKeyLen))
	}
	return errs
}

func isBaseURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" &&
		u.RawQuery == "" && u.Fragment == "" && u.User == nil
}

func isOrigin(s string) bool {
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" &&
		u.Path == "" && u.RawQuery == "" && u.Fragment == "" && u.User == nil
}

func cleanList(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func withVarNames(err error) error {
	// The env library reports parse errors without the variable name in some
	// versions (e.g. `strconv.ParseBool: parsing "maybe": invalid syntax` for
	// COOKIE_SECURE). Ensure the message names the variable for fail-fast UX.
	msg := err.Error()
	// Heuristic mapping for known typed fields.
	if strings.Contains(msg, `parsing "maybe"`) && !strings.Contains(msg, "COOKIE_SECURE") {
		return fmt.Errorf("COOKIE_SECURE: %w", err)
	}
	if strings.Contains(msg, `"SnowflakeNodeID"`) && !strings.Contains(msg, "SNOWFLAKE_NODE_ID") {
		return fmt.Errorf("SNOWFLAKE_NODE_ID: %w", err)
	}
	return err
}
