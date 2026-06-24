package server

import (
	"fmt"
	"strings"

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

// validateCkanCollector fails fast at boot if the [collector.CkanCollector]
// section is missing any attr the analyze path requires (spec §5). Without this,
// a missing/wrong-typed TOML key only surfaces per request as an opaque
// internal_error 500 from the collector's type assertions
// (ckan_collector.go: url/token/verify/ckan_storage_path). Validating here turns
// those latent 500s into a clear, actionable startup error.
func validateCkanCollector(pcConfig *config.Config) error {
	if pcConfig == nil {
		return fmt.Errorf("CkanCollector configuration is missing: PC config is nil")
	}
	cc, ok := pcConfig.Collectors["CkanCollector"]
	if !ok || cc == nil {
		return fmt.Errorf("CkanCollector configuration is missing: add a [collector.CkanCollector] section with url, token, verify and ckan_storage_path attrs")
	}

	// url: required, non-empty string. Single source of the CKAN base URL.
	if url, ok := cc.Attrs["url"].(string); !ok || strings.TrimSpace(url) == "" {
		return fmt.Errorf("CkanCollector.attrs.url is required and must be a non-empty string (the CKAN base URL)")
	}

	// token: required to be present as a string. An empty value is allowed (it
	// means "no server-side token / public-package access"), but the key must be
	// a string so the collector's token type assertion never fails at request time.
	if _, ok := cc.Attrs["token"].(string); !ok {
		return fmt.Errorf("CkanCollector.attrs.token is required and must be a string (use \"\" for no server-side token)")
	}

	// verify: required, must be a bool (TLS-verification policy for CKAN calls).
	if _, ok := cc.Attrs["verify"].(bool); !ok {
		return fmt.Errorf("CkanCollector.attrs.verify is required and must be a bool")
	}

	// ckan_storage_path: required, non-empty string (the mounted FileStore root).
	if p, ok := cc.Attrs["ckan_storage_path"].(string); !ok || strings.TrimSpace(p) == "" {
		return fmt.Errorf("CkanCollector.attrs.ckan_storage_path is required and must be a non-empty string (the mounted CKAN storage root)")
	}

	return nil
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
