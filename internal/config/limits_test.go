package config

import (
	"strings"
	"testing"
)

func TestRequestBodyLimit(t *testing.T) {
	if got := (&Config{}).RequestBodyLimit(); got != DefaultMaxRequestBodyMB<<20 {
		t.Errorf("unset: %d", got)
	}
	if got := (*Config)(nil).RequestBodyLimit(); got != DefaultMaxRequestBodyMB<<20 {
		t.Errorf("nil config: %d", got)
	}
	if got := (&Config{MaxRequestBodyMB: 200}).RequestBodyLimit(); got != 200<<20 {
		t.Errorf("200 MB: %d", got)
	}
}

func TestValidateConfig_MaxRequestBodyMB(t *testing.T) {
	for _, mb := range []int{0, 1, 200, MaxRequestBodyMBCeiling} {
		cfg := validConfig()
		cfg.MaxRequestBodyMB = mb
		if err := validateConfig(cfg); err != nil {
			t.Errorf("max_request_body_mb %d: %v", mb, err)
		}
	}
	for _, mb := range []int{-1, MaxRequestBodyMBCeiling + 1} {
		cfg := validConfig()
		cfg.MaxRequestBodyMB = mb
		if err := validateConfig(cfg); err == nil || !strings.Contains(err.Error(), "max_request_body_mb") {
			t.Errorf("max_request_body_mb %d: err = %v", mb, err)
		}
	}
}
