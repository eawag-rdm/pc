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

	// CKANBaseURL is the CKAN instance URL for authentication
	// If empty, will be read from the PC config
	CKANBaseURL string

	// VerifyTLS controls whether to verify TLS certificates for CKAN API calls
	VerifyTLS bool
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

// GetCKANBaseURL returns the CKAN base URL, either from server config or PC config
func (c Config) GetCKANBaseURL(pcConfig *config.Config) string {
	if c.CKANBaseURL != "" {
		return c.CKANBaseURL
	}

	// Try to get from PC config
	if ckanCollector, ok := pcConfig.Collectors["CkanCollector"]; ok {
		if url, ok := ckanCollector.Attrs["url"].(string); ok {
			return url
		}
	}

	return ""
}

// GetVerifyTLS returns whether TLS should be verified for CKAN API calls
func (c Config) GetVerifyTLS(pcConfig *config.Config) bool {
	// If explicitly set in server config, use that
	if c.VerifyTLS {
		return true
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
