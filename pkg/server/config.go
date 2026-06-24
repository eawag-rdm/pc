package server

import (
	"fmt"

	"github.com/eawag-rdm/pc/pkg/config"
)

// Config holds server configuration
type Config struct {
	// Address is the server listen address (e.g., ":8080")
	Address string

	// ConfigPath is the path to the PC config file (pc.toml)
	ConfigPath string

	// VerifyTLS optionally overrides TLS verification for CKAN API calls.
	// nil means "not set" — fall back to the PC config (and finally the
	// secure default). A non-nil value is honored exactly, so that an
	// explicit false (verify=false) disables verification.
	VerifyTLS *bool
}

// Validate ensures configuration is valid
func (c Config) Validate() error {
	if c.Address == "" {
		return fmt.Errorf("server address is required")
	}
	if c.ConfigPath == "" {
		return fmt.Errorf("PC config path is required")
	}
	return nil
}

// LoadPCConfig loads and returns the PC configuration from the config file
func (c Config) LoadPCConfig() (*config.Config, error) {
	return config.LoadConfig(c.ConfigPath)
}

// GetCKANBaseURL returns the CKAN base URL from the PC config. The collector
// config (`[collector.CkanCollector.attrs] url`) is the single source of truth.
func (c Config) GetCKANBaseURL(pcConfig *config.Config) string {
	if ckanCollector, ok := pcConfig.Collectors["CkanCollector"]; ok {
		if url, ok := ckanCollector.Attrs["url"].(string); ok {
			return url
		}
	}

	return ""
}

// GetVerifyTLS returns whether TLS should be verified for CKAN API calls.
//
// Resolution order:
//  1. an explicit server-config override (c.VerifyTLS != nil) — honored as-is,
//     so verify=false is respected;
//  2. the PC config's CkanCollector "verify" attr;
//  3. the secure default (true).
func (c Config) GetVerifyTLS(pcConfig *config.Config) bool {
	// If explicitly set in server config, honor it exactly (including false).
	if c.VerifyTLS != nil {
		return *c.VerifyTLS
	}

	// Try to get from PC config
	if ckanCollector, ok := pcConfig.Collectors["CkanCollector"]; ok {
		if verify, ok := ckanCollector.Attrs["verify"].(bool); ok {
			return verify
		}
	}

	// Default to true for security
	return true
}
