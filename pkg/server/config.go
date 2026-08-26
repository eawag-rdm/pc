package server

import (
	"fmt"
	"net"
	"net/mail"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/eawag-rdm/pc/pkg/config"
)

// Config holds server configuration
type Config struct {
	// Address optionally overrides the listen address. Production runs the
	// server with NO flags and reads the address from the [server] listenAddress
	// TOML key; this field is left empty there. It exists mainly so tests can
	// inject a specific (e.g. ephemeral) address. When empty, ListenAddress()
	// falls back to the TOML value.
	Address string

	// ConfigPath is the path to the PC config file (pc.toml)
	ConfigPath string

	// PCConfig optionally supplies an already-loaded PC config and takes
	// precedence over ConfigPath, for a caller that must read the config before
	// constructing the server (pc-server pins the CPU budget from it). nil means
	// "not set" - LoadPCConfig reads ConfigPath.
	PCConfig *config.Config

	// VerifyTLS optionally overrides TLS verification for CKAN API calls.
	// nil means "not set" - fall back to the PC config (and finally the
	// secure default). A non-nil value is honored exactly, so that an
	// explicit false (verify=false) disables verification.
	VerifyTLS *bool
}

// Validate ensures the bootstrap configuration is valid. The listen address is
// NOT checked here: it lives in the [server] TOML section and is validated by
// validateServerSettings once the PC config is loaded. Address is an optional
// override (mainly for tests), so its absence is fine.
func (c Config) Validate() error {
	if c.ConfigPath == "" {
		return fmt.Errorf("PC config path is required")
	}
	return nil
}

// ListenAddress resolves the effective listen address: the optional server.Config
// override if set (tests), otherwise the [server] listenAddress TOML value.
func (c Config) ListenAddress(pcConfig *config.Config) string {
	if c.Address != "" {
		return c.Address
	}
	if pcConfig != nil && pcConfig.Server != nil {
		return pcConfig.Server.ListenAddress
	}
	return ""
}

// validateServerSettings fails fast at boot if a [server] value is invalid, so
// a bad TOML setting is caught before the server starts rather than surfacing as
// a runtime failure (or a silent mis-binding). It validates the effective listen
// address plus every other [server] setting except the two CIDR lists:
// allowedClients and trustedProxies are validated by the parsePrefixList calls
// New makes beside this one, which turn them into the prefixes the gate and the
// limiter match against.
func validateServerSettings(pcConfig *config.Config, addr string) error {
	if pcConfig == nil || pcConfig.Server == nil {
		return fmt.Errorf("server configuration is missing")
	}
	s := pcConfig.Server

	// Listen address: required, must be a valid host:port (an empty host like
	// ":8080" is allowed and means all interfaces; the port is mandatory).
	if strings.TrimSpace(addr) == "" {
		return fmt.Errorf("server listenAddress is required (set [server] listenAddress in the config)")
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return fmt.Errorf("server listenAddress %q is not a valid host:port: %w", addr, err)
	}

	// Rate-limit budgets: a value of 0 means "no limit for this scope"; negative
	// is nonsensical.
	if s.PerIPRequestsPerHour < 0 {
		return fmt.Errorf("server perIPRequestsPerHour must be >= 0, got %d", s.PerIPRequestsPerHour)
	}
	if s.GlobalRequestsPerHour < 0 {
		return fmt.Errorf("server globalRequestsPerHour must be >= 0, got %d", s.GlobalRequestsPerHour)
	}
	if s.BurstFactor < 0 {
		return fmt.Errorf("server burstFactor must be >= 0, got %v", s.BurstFactor)
	}
	if s.CachedRequestLimitFactor < 0 {
		return fmt.Errorf("server cachedRequestLimitFactor must be >= 0, got %d", s.CachedRequestLimitFactor)
	}
	if s.AnalysisBusyWaitSeconds < 1 {
		return fmt.Errorf("server analysisBusyWaitSeconds must be >= 1, got %d", s.AnalysisBusyWaitSeconds)
	}
	if s.MaxTrackedRateKeys <= 0 {
		return fmt.Errorf("server maxTrackedRateKeys must be > 0, got %d", s.MaxTrackedRateKeys)
	}
	if s.RequestTimeoutSeconds <= 0 {
		return fmt.Errorf("server requestTimeoutSeconds must be > 0, got %d", s.RequestTimeoutSeconds)
	}
	if s.CkanRequestTimeoutSeconds <= 0 {
		return fmt.Errorf("server ckanRequestTimeoutSeconds must be > 0, got %d", s.CkanRequestTimeoutSeconds)
	}
	if s.CkanRequestTimeoutSeconds > s.RequestTimeoutSeconds {
		return fmt.Errorf("server ckanRequestTimeoutSeconds (%d) must not exceed requestTimeoutSeconds (%d): the CKAN call runs inside the whole-analysis deadline", s.CkanRequestTimeoutSeconds, s.RequestTimeoutSeconds)
	}

	// Result cache: settings only matter when a cache dir is set (empty = the
	// safe disabled default).
	if s.ResultCacheDir != "" {
		// Absolute only: the dir drives a startup delete of its entries subdir,
		// which a relative path would resolve against the process working dir.
		if !filepath.IsAbs(s.ResultCacheDir) {
			return fmt.Errorf("server resultCacheDir %q must be an absolute path (its entries subdirectory is deleted at every start)", s.ResultCacheDir)
		}
		if s.ResultCacheMaxEntries <= 0 {
			return fmt.Errorf("server resultCacheMaxEntries must be > 0 when resultCacheDir is set, got %d", s.ResultCacheMaxEntries)
		}
		if s.ResultCacheMaxAgeHours < 0 {
			return fmt.Errorf("server resultCacheMaxAgeHours must be >= 0, got %d", s.ResultCacheMaxAgeHours)
		}
	}

	// Allowed origins must be absolute URLs (scheme + host) so CORS matching is
	// well-defined.
	for _, origin := range s.AllowedOrigins {
		u, err := url.Parse(origin)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("server allowedOrigins entry %q is not a valid origin URL (e.g. https://app.example.org)", origin)
		}
	}

	// SMTP admin alerts: validate only when ENABLED (host set). An empty host
	// disables alerts, so from/to are not required and are not checked. When the
	// host IS set, the relay must be fully usable so a fault always reaches the
	// admins rather than silently failing at send time.
	if s.SMTP != nil && strings.TrimSpace(s.SMTP.Host) != "" {
		if s.SMTP.Port < 1 || s.SMTP.Port > 65535 {
			return fmt.Errorf("server smtp port must be between 1 and 65535, got %d", s.SMTP.Port)
		}
		if _, err := mail.ParseAddress(s.SMTP.From); err != nil {
			return fmt.Errorf("server smtp from %q is not a valid email address", s.SMTP.From)
		}
		if len(s.SMTP.To) == 0 {
			return fmt.Errorf("server smtp to requires at least one recipient when smtp host is set")
		}
		for _, addr := range s.SMTP.To {
			if _, err := mail.ParseAddress(addr); err != nil {
				return fmt.Errorf("server smtp to entry %q is not a valid email address", addr)
			}
		}
	}

	return nil
}

// parsePrefixList turns one [server] CIDR list into prefixes: allowedClients,
// matched against the client IP the limiter derives, or trustedProxies, matched
// against the connection's peer address. key names the offending list in every
// error. The grammar is CIDR-only and every entry must parse: a bad entry is an
// error (and so a boot failure), never a dropped entry. Dropping one would
// silently change who can reach /analyze - locking out the client it was meant
// to admit, or, if it was the only entry, everyone - or silently withdraw a
// proxy's trust, collapsing every client behind it into one rate-limit bucket.
// An empty list yields no prefixes, and the two read that state oppositely: no
// allowedClients admits every client (the gate is off), no trustedProxies trusts
// no peer (every X-Real-IP is ignored).
func parsePrefixList(key string, cidrs []string) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(cidrs))
	for _, cidr := range cidrs {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(cidr))
		if err != nil {
			return nil, fmt.Errorf("server %s entry %q is not a valid CIDR: %w", key, cidr, err)
		}
		// The matcher unmaps the address before comparing, so an IPv4-mapped
		// prefix can never contain one: a list of only such entries would boot
		// clean and match nobody.
		if prefix.Addr().Is4In6() {
			// From /96 down the prefix covers only the mapped IPv4 space, so the
			// entry the operator meant can be named exactly; wider ones cannot. The
			// suggestion is masked, or it would trip the host-bits check below.
			if prefix.Bits() >= 96 {
				plain := netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96).Masked()
				return nil, fmt.Errorf("server %s entry %q is an IPv4-mapped IPv6 prefix, which no address can match: write it as %q", key, cidr, plain)
			}
			return nil, fmt.Errorf("server %s entry %q is an IPv4-mapped IPv6 prefix, which no address can match: write it as plain IPv4", key, cidr)
		}
		// Host bits are refused rather than masked away: only the operator knows
		// whether "192.0.2.7/24" meant that one host or its whole /24, and masking
		// would silently widen the entry to 254 more of them.
		if masked := prefix.Masked(); masked != prefix {
			host := netip.PrefixFrom(prefix.Addr(), prefix.Addr().BitLen())
			return nil, fmt.Errorf("server %s entry %q has host bits set: write %q for the network, or %q for the single host", key, cidr, masked, host)
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes, nil
}

// LoadPCConfig returns the preloaded PC configuration if one was supplied,
// otherwise loads it from the config file.
func (c Config) LoadPCConfig() (*config.Config, error) {
	if c.PCConfig != nil {
		return c.PCConfig, nil
	}
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

// scrubConfigToken blanks the CkanCollector token attr in the server's loaded
// PC config. The TOML token exists for the CLI only: the server authenticates
// every upstream CKAN call with the request's Bearer token (or anonymously
// when none is sent) and must NEVER fall back to the operator token. The
// analyze path already overrides the attr per request
// (deepCopyConfigForRequest) and the readiness probe is deliberately
// tokenless; blanking the value at boot makes the guarantee structural - no
// present or future server code path can send a token that is no longer in
// memory. Called by New after validateCkanCollector (which only requires the
// key to EXIST as a string; "" stays valid).
func scrubConfigToken(pcConfig *config.Config) {
	if cc, ok := pcConfig.Collectors["CkanCollector"]; ok && cc != nil {
		cc.Attrs["token"] = ""
	}
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
//  1. an explicit server-config override (c.VerifyTLS != nil) - honored as-is,
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
