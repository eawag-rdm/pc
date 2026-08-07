package config

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// Structures for final parsed configuration
type TestConfig struct {
	Blacklist        []string
	Whitelist        []string
	KeywordArguments []map[string]interface{}
	// Attrs holds check-specific scalar settings (e.g. the [test.IsFreeOfSecrets]
	// attrs table). Values are stored verbatim; ValidateChecksConfig type-checks
	// the keys each check dereferences so wrong-typed values fail at config load.
	Attrs map[string]interface{}
}

type CollectorConfig struct {
	Attrs map[string]interface{}
}

type OperationConfig struct {
	Collector string
}

// Default intro text for summary when not configured
const DefaultSummaryIntroText = "We have analyzed your data package and found a few issues. Please address them and get back to us once you're done. Then, we can continue with the publication process. Feel free to get back to us, if something is unclear."

// Default truncation settings for summary
const (
	DefaultSummaryMaxIssuesBeforeTruncation = 5 // Number of issues to show before truncating
	DefaultSummaryMinGroupSizeForTruncation = 3 // Minimum group size to trigger truncation
)

// Default archive-unpacking limits (see GeneralConfig.ArchiveLimits).
const (
	DefaultMaxArchiveFileSize     = 10 * 1024 * 1024   // per archive member (bytes)
	DefaultMaxTotalArchiveMemory  = 100 * 1024 * 1024  // per archive (bytes)
	DefaultMaxArchiveMemberCount  = 1000               // unpack candidates per archive
	DefaultMaxContentScanFileSize = 1024 * 1024 * 1024 // whole-file content scan gate (bytes)
	DefaultMaxPDFPages            = 10                 // page ceiling per PDF; a longer document is skipped WHOLE, not truncated
	DefaultMaxPDFFileSize         = 1024 * 1024        // PDF admission gate (bytes); larger PDFs are not read at all
)

type GeneralConfig struct {
	MaxArchiveFileSize               int64  // Maximum size for individual files in archives (bytes)
	MaxTotalArchiveMemory            int64  // Maximum total memory for archive processing (bytes)
	MaxArchiveMemberCount            int    // Max unpack-candidate members per archive for content checks (0 = default, not unlimited; enforced from C4)
	MaxPDFPages                      int    // Page ceiling per PDF; over it nothing is extracted (0 = default, not unlimited)
	MaxPDFFileSize                   int64  // PDF admission gate in bytes; over it nothing is read (0 = default, not unlimited)
	MaxContentScanFileSize           int64  // Maximum size for files that read content (like IsFreeOfKeywords) (bytes)
	SummaryIntroText                 string // Introductory text shown at the top of the summary
	SummaryMaxIssuesBeforeTruncation int    // Number of issues to show before truncating
	SummaryMinGroupSizeForTruncation int    // Minimum group size to trigger truncation
}

// EffectiveMaxPDFPages applies the documented default to non-positive values
// (same single-defaulting-site convention as ArchiveLimits; 0 never means
// unlimited).
func (g *GeneralConfig) EffectiveMaxPDFPages() int {
	if g.MaxPDFPages <= 0 {
		return DefaultMaxPDFPages
	}
	return g.MaxPDFPages
}

// EffectiveMaxPDFFileSize applies the documented default to non-positive
// values (same single-defaulting-site convention; 0 never means unlimited).
func (g *GeneralConfig) EffectiveMaxPDFFileSize() int64 {
	if g.MaxPDFFileSize <= 0 {
		return DefaultMaxPDFFileSize
	}
	return g.MaxPDFFileSize
}

// ArchiveLimits returns the effective per-archive unpacking limits, applying
// the documented defaults to non-positive values. This is the ONLY defaulting
// site: it also covers GeneralConfig values hand-built in code that never pass
// ParseConfig, so 0 never means "unlimited". The MaxContentScanFileSize
// whole-file gates are deliberately not part of these limits.
func (g *GeneralConfig) ArchiveLimits() (memberSize int64, totalMemory int64, memberCount int) {
	memberSize = g.MaxArchiveFileSize
	if memberSize <= 0 {
		memberSize = DefaultMaxArchiveFileSize
	}
	totalMemory = g.MaxTotalArchiveMemory
	if totalMemory <= 0 {
		totalMemory = DefaultMaxTotalArchiveMemory
	}
	memberCount = g.MaxArchiveMemberCount
	if memberCount <= 0 {
		memberCount = DefaultMaxArchiveMemberCount
	}
	return memberSize, totalMemory, memberCount
}

// Default values for the [server] section.
const (
	DefaultServerListenAddress             = "127.0.0.1:8080"
	DefaultServerPerIPRequestsPerHour      = 4
	DefaultServerGlobalRequestsPerHour     = 20
	DefaultServerCachedRequestLimitFactor  = 100 // cache hits count against budgets × this factor
	DefaultServerBurstFactor               = 0.5
	DefaultServerAnalysisBusyWaitSeconds   = 2
	DefaultServerMaxTrackedRateKeys        = 10000
	DefaultServerContactMessage            = "If you can't resolve this yourself, please contact rdm@eawag.ch."
	DefaultServerLogClientIP               = true
	DefaultServerTrustProxyHeaders         = true
	DefaultServerRequestTimeoutSeconds     = 300
	DefaultServerCkanRequestTimeoutSeconds = 10
	DefaultServerResultCacheMaxEntries     = 500
	DefaultServerResultCacheMaxAgeHours    = 0 // no age limit: metadata_modified alone keys freshness
)

// Default values for the [server.smtp] sub-section.
const (
	DefaultServerSMTPPort = 25
)

// SMTPConfig holds the [server.smtp] sub-section: a plain SMTP relay (no auth)
// used to email admins when the server returns a server-fault response
// (internal_error, recovered panic, or resource_unreadable). Every such fault
// is reported - there is no cap. Alerts are DISABLED unless Host is set and To
// is non-empty.
type SMTPConfig struct {
	Host string   // SMTP relay host; empty disables admin alerts
	Port int      // SMTP relay port (default 25)
	From string   // From / envelope-sender address for alert mails
	To   []string // admin recipient addresses
}

// ServerConfig holds the configuration for the HTTP server (the [server] section).
type ServerConfig struct {
	ListenAddress             string      // Address the server listens on (host:port). The server takes no flags; this is the sole source.
	TrustProxyHeaders         bool        // Whether to trust proxy-set client IP headers
	TrustedProxies            []string    // CIDRs allowed to set X-Real-IP
	AllowedOrigins            []string    // CORS allow-list of origin URLs
	PerIPRequestsPerHour      int         // Per-IP hourly request budget
	GlobalRequestsPerHour     int         // Global hourly request budget
	CachedRequestLimitFactor  int         // Cache-hit requests are refunded and counted against budgets × this factor; 0 disables the refund
	BurstFactor               float64     // Additional headroom factor applied to budgets
	AnalysisBusyWaitSeconds   int         // Seconds a busy request waits for the analysis gate before 503 (must be >= 1; default 2)
	MaxTrackedRateKeys        int         // Limiter memory bound (max tracked rate keys)
	ContactMessage            string      // Contact suffix shown in error envelopes
	LogClientIP               bool        // Whether to log the client IP
	RequestTimeoutSeconds     int         // Hard upper bound for a whole analysis request (CKAN call + checks), in seconds
	CkanRequestTimeoutSeconds int         // Upper bound for the single CKAN package_show call, in seconds (must be <= RequestTimeoutSeconds)
	ResultCacheDir            string      // Directory for the per-package result cache; empty disables caching
	ResultCacheMaxEntries     int         // Max cached packages before oldest-entry eviction (default 500)
	ResultCacheMaxAgeHours    int         // TTL for cache entries in hours; 0 (the default) disables the TTL
	SMTP                      *SMTPConfig // Optional [server.smtp] admin-alert relay (nil-safe; disabled unless Host+To set)
}

type Config struct {
	General    *GeneralConfig
	Server     *ServerConfig
	Tests      map[string]*TestConfig
	Operation  map[string]*OperationConfig
	Collectors map[string]*CollectorConfig
}

// ParseConfigNew parses the TOML file into a ConfigNew structure
func ParseConfig(filename string) (*Config, error) {
	var raw map[string]interface{}
	if _, err := toml.DecodeFile(filename, &raw); err != nil {
		return nil, err
	}

	c := &Config{
		General: &GeneralConfig{
			MaxArchiveFileSize:               DefaultMaxArchiveFileSize,
			MaxTotalArchiveMemory:            DefaultMaxTotalArchiveMemory,
			MaxArchiveMemberCount:            DefaultMaxArchiveMemberCount,
			MaxPDFPages:                      DefaultMaxPDFPages,
			MaxPDFFileSize:                   DefaultMaxPDFFileSize,
			MaxContentScanFileSize:           DefaultMaxContentScanFileSize,
			SummaryIntroText:                 DefaultSummaryIntroText,
			SummaryMaxIssuesBeforeTruncation: DefaultSummaryMaxIssuesBeforeTruncation,
			SummaryMinGroupSizeForTruncation: DefaultSummaryMinGroupSizeForTruncation,
		},
		Server: &ServerConfig{
			ListenAddress:             DefaultServerListenAddress,
			TrustProxyHeaders:         DefaultServerTrustProxyHeaders,
			TrustedProxies:            nil,
			AllowedOrigins:            nil,
			PerIPRequestsPerHour:      DefaultServerPerIPRequestsPerHour,
			GlobalRequestsPerHour:     DefaultServerGlobalRequestsPerHour,
			CachedRequestLimitFactor:  DefaultServerCachedRequestLimitFactor,
			BurstFactor:               DefaultServerBurstFactor,
			AnalysisBusyWaitSeconds:   DefaultServerAnalysisBusyWaitSeconds,
			MaxTrackedRateKeys:        DefaultServerMaxTrackedRateKeys,
			ContactMessage:            DefaultServerContactMessage,
			LogClientIP:               DefaultServerLogClientIP,
			RequestTimeoutSeconds:     DefaultServerRequestTimeoutSeconds,
			CkanRequestTimeoutSeconds: DefaultServerCkanRequestTimeoutSeconds,
			ResultCacheDir:            "",
			ResultCacheMaxEntries:     DefaultServerResultCacheMaxEntries,
			ResultCacheMaxAgeHours:    DefaultServerResultCacheMaxAgeHours,
			SMTP: &SMTPConfig{
				Port: DefaultServerSMTPPort,
			},
		},
		Tests:      map[string]*TestConfig{},
		Operation:  map[string]*OperationConfig{},
		Collectors: map[string]*CollectorConfig{},
	}

	parseStringSlice := func(data []interface{}) []string {
		var result []string
		for _, item := range data {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	}

	parseKeywordArguments := func(data []interface{}) []map[string]interface{} {
		var result []map[string]interface{}
		for _, kwItem := range data {
			if kwMap, ok := kwItem.(map[string]interface{}); ok {
				kwSet := make(map[string]interface{})
				for k, v := range kwMap {
					switch val := v.(type) {
					case string:
						kwSet[k] = val
					case []interface{}:
						kwSet[k] = parseStringSlice(val)
					}
				}
				result = append(result, kwSet)
			}
		}
		return result
	}

	// Parse general section
	if generalData, ok := raw["general"].(map[string]interface{}); ok {
		if maxArchiveFileSize, ok := generalData["maxArchiveFileSize"].(int64); ok {
			c.General.MaxArchiveFileSize = maxArchiveFileSize
		}
		if maxTotalArchiveMemory, ok := generalData["maxTotalArchiveMemory"].(int64); ok {
			c.General.MaxTotalArchiveMemory = maxTotalArchiveMemory
		}
		if maxContentScanFileSize, ok := generalData["maxContentScanFileSize"].(int64); ok {
			c.General.MaxContentScanFileSize = maxContentScanFileSize
		}
		// Newer fail-fast convention (wrong type = load error, unlike the
		// silent asserts above; those migrate in a separate commit).
		if err := generalInt(generalData, "maxArchiveMemberCount", &c.General.MaxArchiveMemberCount); err != nil {
			return nil, err
		}
		if c.General.MaxArchiveMemberCount < 0 {
			return nil, fmt.Errorf("[general] maxArchiveMemberCount must be >= 0, got %d", c.General.MaxArchiveMemberCount)
		}
		if err := generalInt(generalData, "maxPDFPages", &c.General.MaxPDFPages); err != nil {
			return nil, err
		}
		if c.General.MaxPDFPages < 0 {
			return nil, fmt.Errorf("[general] maxPDFPages must be >= 0, got %d", c.General.MaxPDFPages)
		}
		if err := generalInt64(generalData, "maxPDFFileSize", &c.General.MaxPDFFileSize); err != nil {
			return nil, err
		}
		if c.General.MaxPDFFileSize < 0 {
			return nil, fmt.Errorf("[general] maxPDFFileSize must be >= 0, got %d", c.General.MaxPDFFileSize)
		}
		if summaryIntroText, ok := generalData["summaryIntroText"].(string); ok {
			c.General.SummaryIntroText = summaryIntroText
		}
		if val, ok := generalData["summaryMaxIssuesBeforeTruncation"].(int64); ok {
			c.General.SummaryMaxIssuesBeforeTruncation = int(val)
		}
		if val, ok := generalData["summaryMinGroupSizeForTruncation"].(int64); ok {
			c.General.SummaryMinGroupSizeForTruncation = int(val)
		}
	}

	// Parse server section. Each [server] / [server.smtp] key is read through a
	// typed getter that overrides the default only when the key is PRESENT and of
	// the right type. A present-but-wrong-typed key returns an error (fail fast)
	// rather than being silently skipped, so an operator who wrong-types a setting
	// (e.g. perIPRequestsPerHour = "4") is told instead of silently keeping the
	// default. A missing key leaves the struct-literal default untouched.
	if serverData, ok := raw["server"].(map[string]interface{}); ok {
		if err := serverString(serverData, "listenAddress", &c.Server.ListenAddress); err != nil {
			return nil, err
		}
		if err := serverBool(serverData, "trustProxyHeaders", &c.Server.TrustProxyHeaders); err != nil {
			return nil, err
		}
		if err := serverStringSlice(serverData, "trustedProxies", &c.Server.TrustedProxies); err != nil {
			return nil, err
		}
		if err := serverStringSlice(serverData, "allowedOrigins", &c.Server.AllowedOrigins); err != nil {
			return nil, err
		}
		if err := serverInt(serverData, "perIPRequestsPerHour", &c.Server.PerIPRequestsPerHour); err != nil {
			return nil, err
		}
		if err := serverInt(serverData, "globalRequestsPerHour", &c.Server.GlobalRequestsPerHour); err != nil {
			return nil, err
		}
		if err := serverInt(serverData, "cachedRequestLimitFactor", &c.Server.CachedRequestLimitFactor); err != nil {
			return nil, err
		}
		// burstFactor may be expressed as a TOML float (0.5) or a bare int (1, 2).
		if err := serverFloat(serverData, "burstFactor", &c.Server.BurstFactor); err != nil {
			return nil, err
		}
		if err := serverInt(serverData, "analysisBusyWaitSeconds", &c.Server.AnalysisBusyWaitSeconds); err != nil {
			return nil, err
		}
		if err := serverInt(serverData, "maxTrackedRateKeys", &c.Server.MaxTrackedRateKeys); err != nil {
			return nil, err
		}
		if err := serverString(serverData, "contactMessage", &c.Server.ContactMessage); err != nil {
			return nil, err
		}
		if err := serverBool(serverData, "logClientIP", &c.Server.LogClientIP); err != nil {
			return nil, err
		}
		if err := serverInt(serverData, "requestTimeoutSeconds", &c.Server.RequestTimeoutSeconds); err != nil {
			return nil, err
		}
		if err := serverInt(serverData, "ckanRequestTimeoutSeconds", &c.Server.CkanRequestTimeoutSeconds); err != nil {
			return nil, err
		}
		if err := serverString(serverData, "resultCacheDir", &c.Server.ResultCacheDir); err != nil {
			return nil, err
		}
		if err := serverInt(serverData, "resultCacheMaxEntries", &c.Server.ResultCacheMaxEntries); err != nil {
			return nil, err
		}
		if err := serverInt(serverData, "resultCacheMaxAgeHours", &c.Server.ResultCacheMaxAgeHours); err != nil {
			return nil, err
		}
		// [server.smtp] sub-section: plain relay for admin alerts on server faults.
		if smtpRaw, present := serverData["smtp"]; present {
			smtpData, ok := smtpRaw.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("[server.smtp] must be a table, got %T", smtpRaw)
			}
			if err := serverString(smtpData, "host", &c.Server.SMTP.Host); err != nil {
				return nil, err
			}
			if err := serverInt(smtpData, "port", &c.Server.SMTP.Port); err != nil {
				return nil, err
			}
			if err := serverString(smtpData, "from", &c.Server.SMTP.From); err != nil {
				return nil, err
			}
			if err := serverStringSlice(smtpData, "to", &c.Server.SMTP.To); err != nil {
				return nil, err
			}
		}
	}

	if testData, ok := raw["test"].(map[string]interface{}); ok {
		for name, section := range testData {
			tc := &TestConfig{}
			if sectionMap, ok := section.(map[string]interface{}); ok {
				if bl, ok := sectionMap["blacklist"].([]interface{}); ok {
					tc.Blacklist = parseStringSlice(bl)
				}
				if wl, ok := sectionMap["whitelist"].([]interface{}); ok {
					tc.Whitelist = parseStringSlice(wl)
				}
				if kwArgs, ok := sectionMap["keywordArguments"].([]interface{}); ok {
					tc.KeywordArguments = parseKeywordArguments(kwArgs)
				}
				if attrs, ok := sectionMap["attrs"].(map[string]interface{}); ok {
					tc.Attrs = attrs
				}
			}
			c.Tests[name] = tc
		}
	}

	if collectorData, ok := raw["collector"].(map[string]interface{}); ok {
		for name, section := range collectorData {
			cc := &CollectorConfig{Attrs: make(map[string]interface{})}
			if sectionMap, ok := section.(map[string]interface{}); ok {
				if attrs, ok := sectionMap["attrs"].(map[string]interface{}); ok {
					for k, v := range attrs {
						switch val := v.(type) {
						case string:
							cc.Attrs[k] = val
						case bool:
							cc.Attrs[k] = val
						case int64:
							cc.Attrs[k] = val
						case []interface{}:
							cc.Attrs[k] = parseStringSlice(val)
						default:
							// Fail fast instead of silently dropping the attr
							// (an integer attr used to vanish here).
							return nil, fmt.Errorf("[collector.%s] attrs.%s has unsupported type %T", name, k, v)
						}
					}
				}
			}
			c.Collectors[name] = cc
		}
	}

	if operationData, ok := raw["operation"].(map[string]interface{}); ok {
		for name, section := range operationData {
			oc := &OperationConfig{}
			if sectionMap, ok := section.(map[string]interface{}); ok {
				if collector, ok := sectionMap["collector"].(string); ok {
					oc.Collector = collector
				}
			}
			c.Operation[name] = oc
		}
	}
	return c, nil
}

// The serverXxx helpers read a single [server] / [server.smtp] key from the
// decoded TOML table. Each OVERRIDES *dst only when the key is PRESENT and of the
// expected type; a missing key is a no-op (the struct-literal default stands). A
// present-but-wrong-typed key returns a clear error naming the key and expected
// type, so an operator who wrong-types a setting fails fast at config load rather
// than silently keeping the default.

// serverString sets *dst when key holds a string; errors if present but not a string.
func serverString(m map[string]interface{}, key string, dst *string) error {
	v, ok := m[key]
	if !ok {
		return nil
	}
	sv, ok := v.(string)
	if !ok {
		return fmt.Errorf("[server] %s must be a string, got %T", key, v)
	}
	*dst = sv
	return nil
}

// tomlInt sets *dst when key holds a TOML integer; errors (naming section and
// key) if present but not an integer. Missing key is a no-op.
func tomlInt(section string, m map[string]interface{}, key string, dst *int) error {
	v, ok := m[key]
	if !ok {
		return nil
	}
	iv, ok := v.(int64)
	if !ok {
		return fmt.Errorf("[%s] %s must be an integer, got %T", section, key, v)
	}
	*dst = int(iv)
	return nil
}

// serverInt sets *dst when key holds a TOML integer; errors if present but not an integer.
func serverInt(m map[string]interface{}, key string, dst *int) error {
	return tomlInt("server", m, key, dst)
}

// generalInt reads a [general] integer key with the fail-fast convention.
func generalInt(m map[string]interface{}, key string, dst *int) error {
	return tomlInt("general", m, key, dst)
}

// generalInt64 is generalInt for byte-size keys, which exceed int range on
// 32-bit builds and so keep their int64 width.
func generalInt64(m map[string]interface{}, key string, dst *int64) error {
	v, ok := m[key]
	if !ok {
		return nil
	}
	iv, ok := v.(int64)
	if !ok {
		return fmt.Errorf("[general] %s must be an integer, got %T", key, v)
	}
	*dst = iv
	return nil
}

// serverBool sets *dst when key holds a bool; errors if present but not a bool.
func serverBool(m map[string]interface{}, key string, dst *bool) error {
	v, ok := m[key]
	if !ok {
		return nil
	}
	bv, ok := v.(bool)
	if !ok {
		return fmt.Errorf("[server] %s must be a boolean, got %T", key, v)
	}
	*dst = bv
	return nil
}

// serverFloat sets *dst when key holds a TOML float OR a bare integer (so a value
// like 1 is accepted as 1.0); errors if present but neither.
func serverFloat(m map[string]interface{}, key string, dst *float64) error {
	v, ok := m[key]
	if !ok {
		return nil
	}
	switch fv := v.(type) {
	case float64:
		*dst = fv
	case int64:
		*dst = float64(fv)
	default:
		return fmt.Errorf("[server] %s must be a number, got %T", key, v)
	}
	return nil
}

// serverStringSlice sets *dst when key holds a TOML array of strings. It errors if
// the key is present but not an array, OR if any element is not a string. Unlike
// the lenient shared parseStringSlice (which silently drops non-string elements),
// this validates every element so an operator typo like trustedProxies = [1, 2]
// fails fast at config load instead of yielding a silently empty/partial slice. A
// missing key is a no-op (the struct-literal default stands).
func serverStringSlice(m map[string]interface{}, key string, dst *[]string) error {
	v, ok := m[key]
	if !ok {
		return nil
	}
	av, ok := v.([]interface{})
	if !ok {
		return fmt.Errorf("[server] %s must be an array of strings, got %T", key, v)
	}
	result := make([]string, 0, len(av))
	for _, item := range av {
		s, ok := item.(string)
		if !ok {
			return fmt.Errorf("[server] %s must be an array of strings, got a %T element", key, item)
		}
		result = append(result, s)
	}
	*dst = result
	return nil
}

// assesLists checks that there is no overlap between blacklist and whitelist
// and ensures that only one of the two is defined
func assesLists(blacklist []string, whitelist []string) error {
	if !((len(blacklist) > 0 && len(whitelist) == 0) ||
		(len(blacklist) == 0 && len(whitelist) > 0) ||
		(len(blacklist) == 0 && len(whitelist) == 0)) {
		return fmt.Errorf("only one is allowed to have entries. Either the blacklist OR the whitelist")
	}
	return nil
}

// LoadConfig loads the configuration from a TOML file and performs the necessary checks
func LoadConfig(file string) (*Config, error) {
	var config *Config
	config, err := ParseConfig(file)
	if err != nil {
		return nil, fmt.Errorf("failed to parse config file '%s': %w", file, err)
	}

	for testName, test := range config.Tests {
		if err := assesLists(test.Blacklist, test.Whitelist); err != nil {
			return nil, fmt.Errorf("error in test %s: %v", testName, err)
		}
	}

	return config, nil
}

// FindConfigFile returns ./pc.toml if it exists, otherwise "". (Tilde-prefixed
// home paths were listed here historically but never worked: os.Stat sees a
// literal "~", so only the working-directory file could ever match.)
func FindConfigFile() string {
	if _, err := os.Stat("./pc.toml"); err == nil {
		return "./pc.toml"
	}
	return ""
}
