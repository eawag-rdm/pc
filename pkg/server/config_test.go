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
			name: "missing address",
			config: Config{
				Address:    "",
				ConfigPath: "/path/to/pc.toml",
			},
			wantErr: true,
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
			name: "both missing",
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
