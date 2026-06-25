package server

import (
	"strings"
	"testing"

	"github.com/eawag-rdm/pc/pkg/config"
)

// ckanCfg builds a PC config whose CkanCollector has exactly the given attrs.
func ckanCfg(attrs map[string]interface{}) *config.Config {
	return &config.Config{
		Collectors: map[string]*config.CollectorConfig{
			"CkanCollector": {Attrs: attrs},
		},
	}
}

// fullCkanAttrs returns a complete, valid set of CkanCollector attrs.
func fullCkanAttrs() map[string]interface{} {
	return map[string]interface{}{
		"url":               "https://ckan.example.org",
		"token":             "",
		"verify":            false,
		"ckan_storage_path": "/srv/ckan/storage",
	}
}

// TestValidateCkanCollector_FailFast asserts that a missing/wrong-typed required
// CkanCollector attr is caught at validation time with a clear error naming the
// offending attr (so it fails at BOOT instead of as a per-request 500).
func TestValidateCkanCollector_FailFast(t *testing.T) {
	t.Run("complete config is valid", func(t *testing.T) {
		if err := validateCkanCollector(ckanCfg(fullCkanAttrs())); err != nil {
			t.Fatalf("expected valid config, got error: %v", err)
		}
	})

	t.Run("nil config", func(t *testing.T) {
		if err := validateCkanCollector(nil); err == nil {
			t.Fatal("expected error for nil config")
		}
	})

	t.Run("missing CkanCollector section", func(t *testing.T) {
		if err := validateCkanCollector(&config.Config{}); err == nil {
			t.Fatal("expected error for missing CkanCollector")
		}
	})

	cases := []struct {
		name    string
		mutate  func(m map[string]interface{})
		wantSub string
	}{
		{"missing url", func(m map[string]interface{}) { delete(m, "url") }, "url"},
		{"empty url", func(m map[string]interface{}) { m["url"] = "" }, "url"},
		{"wrong-type url", func(m map[string]interface{}) { m["url"] = 42 }, "url"},
		{"missing token", func(m map[string]interface{}) { delete(m, "token") }, "token"},
		{"wrong-type token", func(m map[string]interface{}) { m["token"] = true }, "token"},
		{"missing verify", func(m map[string]interface{}) { delete(m, "verify") }, "verify"},
		{"wrong-type verify", func(m map[string]interface{}) { m["verify"] = "yes" }, "verify"},
		{"missing storage path", func(m map[string]interface{}) { delete(m, "ckan_storage_path") }, "ckan_storage_path"},
		{"empty storage path", func(m map[string]interface{}) { m["ckan_storage_path"] = "  " }, "ckan_storage_path"},
		{"wrong-type storage path", func(m map[string]interface{}) { m["ckan_storage_path"] = 1 }, "ckan_storage_path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			attrs := fullCkanAttrs()
			tc.mutate(attrs)
			err := validateCkanCollector(ckanCfg(attrs))
			if err == nil {
				t.Fatalf("expected a startup error for %q", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not name the offending attr %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr bool
	}{
		{
			name: "valid config",
			config: Config{
				Address:    ":8080",
				ConfigPath: "/path/to/pc.toml",
			},
			wantErr: false,
		},
		{
			name: "address optional (comes from TOML)",
			config: Config{
				Address:    "",
				ConfigPath: "/path/to/pc.toml",
			},
			wantErr: false,
		},
		{
			name: "missing config path",
			config: Config{
				Address:    ":8080",
				ConfigPath: "",
			},
			wantErr: true,
		},
		{
			name: "config path required even with address",
			config: Config{
				Address:    "",
				ConfigPath: "",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestConfig_GetCKANBaseURL(t *testing.T) {
	// The collector config is the single source of truth for the CKAN URL.
	t.Run("pc config", func(t *testing.T) {
		cfg := Config{}
		pcConfig := &config.Config{
			Collectors: map[string]*config.CollectorConfig{
				"CkanCollector": {
					Attrs: map[string]interface{}{
						"url": "https://ckan.example.com",
					},
				},
			},
		}

		result := cfg.GetCKANBaseURL(pcConfig)
		if result != "https://ckan.example.com" {
			t.Errorf("Expected PC config URL, got %s", result)
		}
	})

	// Test with empty config
	t.Run("empty config", func(t *testing.T) {
		cfg := Config{}
		pcConfig := &config.Config{}

		result := cfg.GetCKANBaseURL(pcConfig)
		if result != "" {
			t.Errorf("Expected empty string, got %s", result)
		}
	})
}

func boolPtr(b bool) *bool { return &b }

func TestConfig_GetVerifyTLS(t *testing.T) {
	// Test with explicit server config override true
	t.Run("server config override true", func(t *testing.T) {
		cfg := Config{VerifyTLS: boolPtr(true)}
		pcConfig := &config.Config{}

		result := cfg.GetVerifyTLS(pcConfig)
		if !result {
			t.Error("Expected true")
		}
	})

	// Explicit server-config override of false must be honored, even if the
	// PC config would otherwise say true. This is the core GetVerifyTLS fix.
	t.Run("server config override false honored", func(t *testing.T) {
		cfg := Config{VerifyTLS: boolPtr(false)}
		pcConfig := &config.Config{
			Collectors: map[string]*config.CollectorConfig{
				"CkanCollector": {
					Attrs: map[string]interface{}{
						"verify": true,
					},
				},
			},
		}

		result := cfg.GetVerifyTLS(pcConfig)
		if result {
			t.Error("Expected false from explicit server override")
		}
	})

	// Test with PC config (no server override) honoring verify=false
	t.Run("pc config false", func(t *testing.T) {
		cfg := Config{}
		pcConfig := &config.Config{
			Collectors: map[string]*config.CollectorConfig{
				"CkanCollector": {
					Attrs: map[string]interface{}{
						"verify": false,
					},
				},
			},
		}

		result := cfg.GetVerifyTLS(pcConfig)
		if result {
			t.Error("Expected false from PC config")
		}
	})

	// Test default (should be true)
	t.Run("default true", func(t *testing.T) {
		cfg := Config{}
		pcConfig := &config.Config{}

		result := cfg.GetVerifyTLS(pcConfig)
		if !result {
			t.Error("Expected default true")
		}
	})
}

// validServerConfig returns a [server] config with all-valid defaults that
// validateServerSettings must accept; tests mutate one field to assert rejection.
func validServerConfig() *config.ServerConfig {
	return &config.ServerConfig{
		ListenAddress:           config.DefaultServerListenAddress,
		TrustProxyHeaders:       true,
		TrustedProxies:          []string{"127.0.0.1/32"},
		AllowedOrigins:          []string{"https://app.example.org"},
		PerIPRequestsPerHour:    config.DefaultServerPerIPRequestsPerHour,
		GlobalRequestsPerHour:   config.DefaultServerGlobalRequestsPerHour,
		BurstFactor:             config.DefaultServerBurstFactor,
		AnalysisBusyWaitSeconds: config.DefaultServerAnalysisBusyWaitSeconds,
		MaxTrackedRateKeys:      config.DefaultServerMaxTrackedRateKeys,
		ContactMessage:          config.DefaultServerContactMessage,
		LogClientIP:             true,
		RequestTimeoutSeconds:   config.DefaultServerRequestTimeoutSeconds,
	}
}

func TestConfig_ListenAddress(t *testing.T) {
	pc := &config.Config{Server: &config.ServerConfig{ListenAddress: "0.0.0.0:9000"}}

	// Override wins when set (test injection).
	if got := (Config{Address: "127.0.0.1:1234"}).ListenAddress(pc); got != "127.0.0.1:1234" {
		t.Errorf("override not honored: got %q", got)
	}
	// Falls back to the TOML value when no override (production).
	if got := (Config{}).ListenAddress(pc); got != "0.0.0.0:9000" {
		t.Errorf("TOML fallback not used: got %q", got)
	}
}

func TestValidateServerSettings(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		mutate  func(s *config.ServerConfig)
		wantErr bool
	}{
		{name: "all valid", addr: "127.0.0.1:8080", wantErr: false},
		{name: "bind-all address valid", addr: ":8080", wantErr: false},
		{name: "empty address", addr: "", wantErr: true},
		{name: "address without port", addr: "127.0.0.1", wantErr: true},
		{name: "garbage address", addr: "not an address", wantErr: true},
		{name: "negative perIP", addr: "127.0.0.1:8080", mutate: func(s *config.ServerConfig) { s.PerIPRequestsPerHour = -1 }, wantErr: true},
		{name: "negative global", addr: "127.0.0.1:8080", mutate: func(s *config.ServerConfig) { s.GlobalRequestsPerHour = -5 }, wantErr: true},
		{name: "zero budgets allowed (unlimited)", addr: "127.0.0.1:8080", mutate: func(s *config.ServerConfig) { s.PerIPRequestsPerHour = 0; s.GlobalRequestsPerHour = 0 }, wantErr: false},
		{name: "negative burst", addr: "127.0.0.1:8080", mutate: func(s *config.ServerConfig) { s.BurstFactor = -0.5 }, wantErr: true},
		{name: "zero maxTrackedRateKeys", addr: "127.0.0.1:8080", mutate: func(s *config.ServerConfig) { s.MaxTrackedRateKeys = 0 }, wantErr: true},
		{name: "zero requestTimeout", addr: "127.0.0.1:8080", mutate: func(s *config.ServerConfig) { s.RequestTimeoutSeconds = 0 }, wantErr: true},
		{name: "invalid CIDR in trustedProxies", addr: "127.0.0.1:8080", mutate: func(s *config.ServerConfig) { s.TrustedProxies = []string{"127.0.0.1"} }, wantErr: true},
		{name: "valid CIDR list", addr: "127.0.0.1:8080", mutate: func(s *config.ServerConfig) { s.TrustedProxies = []string{"10.0.0.0/8", "::1/128"} }, wantErr: false},
		{name: "invalid origin (no scheme)", addr: "127.0.0.1:8080", mutate: func(s *config.ServerConfig) { s.AllowedOrigins = []string{"app.example.org"} }, wantErr: true},
		{name: "empty origins allowed", addr: "127.0.0.1:8080", mutate: func(s *config.ServerConfig) { s.AllowedOrigins = nil }, wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validServerConfig()
			if tt.mutate != nil {
				tt.mutate(s)
			}
			err := validateServerSettings(&config.Config{Server: s}, tt.addr)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateServerSettings() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}

	t.Run("nil server config", func(t *testing.T) {
		if err := validateServerSettings(&config.Config{}, "127.0.0.1:8080"); err == nil {
			t.Error("expected error for nil Server config")
		}
	})
}
