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
	// Test with server config override
	t.Run("server config override", func(t *testing.T) {
		cfg := Config{
			CKANBaseURL: "https://server-override.example.com",
		}
		pcConfig := &config.Config{}

		result := cfg.GetCKANBaseURL(pcConfig)
		if result != "https://server-override.example.com" {
			t.Errorf("Expected server override URL, got %s", result)
		}
	})

	// Test with PC config
	t.Run("pc config", func(t *testing.T) {
		cfg := Config{
			CKANBaseURL: "",
		}
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

func TestConfig_GetVerifyTLS(t *testing.T) {
	// Test with server config true
	t.Run("server config true", func(t *testing.T) {
		cfg := Config{VerifyTLS: true}
		pcConfig := &config.Config{}

		result := cfg.GetVerifyTLS(pcConfig)
		if !result {
			t.Error("Expected true")
		}
	})

	// Test with PC config
	t.Run("pc config false", func(t *testing.T) {
		cfg := Config{VerifyTLS: false}
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
