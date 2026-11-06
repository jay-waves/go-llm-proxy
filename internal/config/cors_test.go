package config

import "testing"

func TestCORSOriginValidation(t *testing.T) {
	for _, origin := range []string{"http://pdf.ts.localhost", "http://epub.ts.localhost/", "https://example.com:8443", "http://127.0.0.1:3000"} {
		if err := validateConfig(&Config{CORSAllowedOrigins: []string{origin}}); err != nil {
			t.Errorf("valid origin %q: %v", origin, err)
		}
	}
	for _, origin := range []string{"*", "http://*.localhost", "null", "example.com", "ftp://example.com", "http://example.com/path", "http://example.com?query", "http://example.com#fragment", "http://user:pass@example.com", "http://"} {
		if err := validateConfig(&Config{CORSAllowedOrigins: []string{origin}}); err == nil {
			t.Errorf("invalid origin accepted: %q", origin)
		}
	}
}
