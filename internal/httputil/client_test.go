package httputil

import (
	"net/http"
	"net/url"
	"testing"
)

// Upstream requests follow HTTP(S)_PROXY / NO_PROXY.
func TestSharedTransportUsesProxyFromEnvironment(t *testing.T) {
	rt, ok := sharedTransport(0).(*connectRoundTripper)
	if !ok {
		t.Fatalf("transport type %T", sharedTransport(0))
	}
	base, ok := rt.base.(*http.Transport)
	if !ok || base.Proxy == nil {
		t.Fatalf("base transport %T has no Proxy function", rt.base)
	}
	// No proxy variables in the test environment: requests go direct.
	u, _ := url.Parse("https://api.example.com/v1/chat/completions")
	if p, err := base.Proxy(&http.Request{URL: u}); err != nil || p != nil {
		t.Fatalf("proxy without environment = %v, %v", p, err)
	}
}
