package config

import (
	"fmt"
	"net/url"
	"strings"
)

func validateCORSOrigins(origins []string) error {
	for _, origin := range origins {
		u, err := url.Parse(origin)
		if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") ||
			u.Hostname() == "" || strings.Contains(u.Host, "*") || u.User != nil ||
			(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
			return fmt.Errorf("cors_allowed_origins: %q must be an exact HTTP(S) origin without wildcards, credentials, query or path", origin)
		}
	}
	return nil
}
