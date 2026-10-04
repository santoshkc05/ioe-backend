package httpserver_test

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
)

func req(remote, xff string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remote
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	return r
}

func TestClientIPIgnoresForwardedForFromUntrustedPeer(t *testing.T) {
	res := httpserver.NewIPResolver([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	if got := res.ClientIP(req("203.0.113.5:4000", "1.2.3.4")); got != "203.0.113.5" {
		t.Fatalf("got %q", got)
	}
}

func TestClientIPUsesRightmostUntrustedHopBehindProxy(t *testing.T) {
	res := httpserver.NewIPResolver([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	if got := res.ClientIP(req("10.0.0.2:4000", "1.2.3.4, 198.51.100.7, 10.0.0.9")); got != "198.51.100.7" {
		t.Fatalf("got %q", got)
	}
}

func TestClientIPFallsBackToPeerOnGarbage(t *testing.T) {
	res := httpserver.NewIPResolver([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	if got := res.ClientIP(req("10.0.0.2:4000", "not-an-ip")); got != "10.0.0.2" {
		t.Fatalf("got %q", got)
	}
}
