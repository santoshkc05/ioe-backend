package httpserver

import (
	"net/http"
	"net/netip"
	"strings"
)

// IPResolver determines the client address, trusting X-Forwarded-For only from known proxies.
type IPResolver struct {
	trusted []netip.Prefix
}

func NewIPResolver(trusted []netip.Prefix) IPResolver { return IPResolver{trusted: trusted} }

// ClientIP returns the peer address, or, when the peer is a trusted proxy, the right-most
// X-Forwarded-For entry that is not itself a trusted proxy.
func (res IPResolver) ClientIP(r *http.Request) string {
	peer := parseRemote(r.RemoteAddr)
	if !peer.IsValid() {
		return ""
	}
	if !res.isTrusted(peer) {
		return peer.String()
	}
	hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		if a = a.Unmap(); !res.isTrusted(a) {
			return a.String()
		}
	}
	return peer.String()
}

func (res IPResolver) isTrusted(a netip.Addr) bool {
	for _, p := range res.trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func parseRemote(remote string) netip.Addr {
	if ap, err := netip.ParseAddrPort(remote); err == nil {
		return ap.Addr().Unmap()
	}
	if a, err := netip.ParseAddr(remote); err == nil {
		return a.Unmap()
	}
	return netip.Addr{}
}
