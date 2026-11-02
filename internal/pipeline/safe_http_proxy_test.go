package pipeline

import (
	"net/http"
	"testing"
)

// Image fetches must never go through an environment proxy: the SSRF check
// runs at dial time and would validate the proxy instead of the image host.
func TestSafeHTTPClientIgnoresEnvironmentProxy(t *testing.T) {
	tr, ok := safeHTTPClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type %T", safeHTTPClient.Transport)
	}
	if tr.Proxy != nil {
		t.Fatal("safeHTTPClient has a Proxy function")
	}
}
