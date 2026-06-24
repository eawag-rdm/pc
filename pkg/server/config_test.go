package server

import (
	"testing"

	"github.com/eawag-rdm/pc/pkg/config"
)

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
