package config

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Structures for final parsed configuration
type TestConfig struct {
	Blacklist        []string
	Whitelist        []string
	KeywordArguments []map[string]interface{}
	// Attrs holds check-specific scalar settings (e.g. the [test.IsFreeOfSecrets]
	// attrs table). Values are stored verbatim; the check's own Bind type-checks
	// them when utils.Compile builds the plan, so wrong-typed values fail at load.
	Attrs map[string]interface{}
}

// DefaultRulePrefix marks the names of synthesized default rules
// (checks.RuleSpecs). parseRuleSpec refuses it on declared rules, so synthesis
// can never collide with an operator's rule name.
const DefaultRulePrefix = "default:"

// RuleSpec is the declarative form of one rule: a named instance of a check
// with its own parameters and its own file selector. It holds strings and raw
// TOML values only and knows nothing about the check it names - pkg/utils
// compiles it against the check registry. [[rule]] sections decode into these;
// the legacy [test.X] sections are translated into them (checks.RuleSpecs)
// until that sugar is removed.
type RuleSpec struct {
	Name       string   // unique, operator-chosen
	Check      string   // registered check name
	Scope      []string // empty: the check's own scopes
	Subject    string   // "name" (default; archive-member and repository default to "path") or "path"
	Enabled    bool     // a disabled rule is left out of the plan
	IgnoreCase bool     // selector case folding
	Include    []string // selector include patterns
	Exclude    []string // selector exclude patterns
	// Params holds the check-specific parameter sets, type-checked by the
	// check's Bind: each [rule.params] table is one set, the repeated
	// [[rule.params]] form carries several, and a translated legacy section's
	// keywordArguments list lands here unchanged.
	Params []map[string]interface{}
	// Attrs is a translated legacy section's attrs table, verbatim; only the
	// leak check reads one. The [[rule]] surface never sets it - its knobs are
	// ordinary params - and it dies with the legacy sugar.
	Attrs map[string]interface{}
	// Legacy marks a spec translated from a [test.X] section: its lists keep
	// the two historical readings (regex over names at dispatch,
	// case-insensitive literals over member paths) until the sugar is removed.
	// The [[rule]] surface never sets it.
	Legacy bool
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
	DefaultMaxCores               = 4                  // CPU cores pc may use; every pool and the leak scanner size from it
)

type GeneralConfig struct {
	MaxArchiveFileSize               int64  // Maximum size for individual files in archives (bytes)
	MaxTotalArchiveMemory            int64  // Maximum total memory for archive processing (bytes)
	MaxArchiveMemberCount            int    // Max members per archive: content scan counts unpack candidates, name walk counts all entries (0 = default, not unlimited)
	MaxPDFPages                      int    // Page ceiling per PDF; over it nothing is extracted (0 = default, not unlimited)
	MaxPDFFileSize                   int64  // PDF admission gate in bytes; over it nothing is read (0 = default, not unlimited)
	MaxContentScanFileSize           int64  // Maximum size for files that read content (like IsFreeOfKeywords) (bytes)
	MaxCores                         int    // CPU cores pc may use, whole process (0 = default, not unlimited)
	SummaryIntroText                 string // Introductory text shown at the top of the summary
	SummaryMaxIssuesBeforeTruncation int    // Number of issues to show before truncating
	SummaryMinGroupSizeForTruncation int    // Minimum group size to trigger truncation
}

// EffectiveMaxCores applies the documented default to non-positive values (same
// single-defaulting-site convention; 0 never means "every core"). It is a
// CEILING, not a request: the caller clamps it to what the process may actually
// run, so a config shared across machines never oversubscribes the small ones.
func (g *GeneralConfig) EffectiveMaxCores() int {
	if g.MaxCores <= 0 {
		return DefaultMaxCores
	}
	return g.MaxCores
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
	Rules      []RuleSpec // [[rule]] sections, in config order
	Operation  map[string]*OperationConfig
	Collectors map[string]*CollectorConfig
}

// ParseConfigNew parses the TOML file into a ConfigNew structure
func ParseConfig(filename string) (*Config, error) {
	var raw map[string]interface{}
	if _, err := toml.DecodeFile(filename, &raw); err != nil {
		return nil, err
	}

	// Unknown top-level tables are refused outright: TOML accepts any table
	// name, so a typo like [[rules]] would silently discard every declared rule
	// and the load would proceed on defaults.
	var unknownTop []string
	for key := range raw {
		switch key {
		case "general", "server", "test", "rule", "collector", "operation":
		default:
			unknownTop = append(unknownTop, key)
		}
	}
	if len(unknownTop) > 0 {
		sort.Strings(unknownTop) // map order is random; the reported keys must not be
		return nil, fmt.Errorf("unknown top-level config key(s): %s", strings.Join(unknownTop, ", "))
	}

	c := &Config{
		General: &GeneralConfig{
			MaxArchiveFileSize:               DefaultMaxArchiveFileSize,
			MaxTotalArchiveMemory:            DefaultMaxTotalArchiveMemory,
			MaxArchiveMemberCount:            DefaultMaxArchiveMemberCount,
			MaxPDFPages:                      DefaultMaxPDFPages,
			MaxPDFFileSize:                   DefaultMaxPDFFileSize,
			MaxContentScanFileSize:           DefaultMaxContentScanFileSize,
			MaxCores:                         DefaultMaxCores,
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
		if err := generalInt(generalData, "maxCores", &c.General.MaxCores); err != nil {
			return nil, err
		}
		if c.General.MaxCores < 0 {
			return nil, fmt.Errorf("[general] maxCores must be >= 0, got %d", c.General.MaxCores)
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
		// Every faulty section at once, in a stable order: map iteration is
		// random, so reporting the first would name a different section per run
		// and a config author would fix them one load at a time.
		var faulty []string
		for name, tc := range c.Tests {
			if err := assesLists(tc.Blacklist, tc.Whitelist); err != nil {
				faulty = append(faulty, name)
			}
		}
		if len(faulty) > 0 {
			sort.Strings(faulty)
			return nil, fmt.Errorf("error in test %s: only one is allowed to have entries. Either the blacklist OR the whitelist", strings.Join(faulty, ", "))
		}
	}

	// [[rule]] sections: the primary check-configuration surface. Decoding is
	// fail-fast throughout - a wrong-typed or unknown key is a load error, never
	// a silently inert setting. Faulty rules are aggregated, so a config author
	// is told all of them at once rather than one per load.
	if rulesRaw, present := raw["rule"]; present {
		ruleTables, ok := rulesRaw.([]map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("[[rule]] must be an array of tables, got %T", rulesRaw)
		}
		c.Rules = make([]RuleSpec, 0, len(ruleTables))
		var ruleErrs []error
		for i, table := range ruleTables {
			spec, err := parseRuleSpec(i, table)
			if err != nil {
				ruleErrs = append(ruleErrs, err)
				continue
			}
			c.Rules = append(c.Rules, spec)
		}
		if len(ruleErrs) > 0 {
			return nil, errors.Join(ruleErrs...)
		}
		// One surface per check: a [[rule]] and a [test.X] section for the SAME
		// check would need a merge policy nobody can remember. Load error
		// instead, with every collision reported at once.
		var surfaceErrs []error
		for _, rule := range c.Rules {
			if _, both := c.Tests[rule.Check]; both {
				surfaceErrs = append(surfaceErrs, fmt.Errorf("check %q is configured by [[rule]] %q AND [test.%s]: use one surface per check", rule.Check, rule.Name, rule.Check))
			}
		}
		if len(surfaceErrs) > 0 {
			return nil, errors.Join(surfaceErrs...)
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
	if err := secretsTimeoutWithinRequestBudget(c); err != nil {
		return nil, err
	}
	return c, nil
}

// parseRuleSpec decodes one [[rule]] table. name and check are required; every
// key is typed fail-fast and an unknown key is a load error, so a typo like
// "includ" cannot silently disable a filter.
func parseRuleSpec(index int, table map[string]interface{}) (RuleSpec, error) {
	label := fmt.Sprintf("[[rule]] #%d", index+1)
	if name, ok := table["name"].(string); ok && name != "" {
		label = fmt.Sprintf("rule %q", name)
	}
	known := map[string]bool{
		"name": true, "check": true, "scope": true, "subject": true,
		"enabled": true, "ignoreCase": true, "include": true, "exclude": true,
		"params": true,
	}
	keys := make([]string, 0, len(table))
	for key := range table {
		keys = append(keys, key)
	}
	sort.Strings(keys) // map order is random; the reported key must not be
	for _, key := range keys {
		if !known[key] {
			return RuleSpec{}, fmt.Errorf("%s: unknown key %q", label, key)
		}
	}

	str := func(key string, dst *string) error {
		v, ok := table[key]
		if !ok {
			return nil
		}
		sv, ok := v.(string)
		if !ok {
			return fmt.Errorf("%s: %s must be a string, got %T", label, key, v)
		}
		*dst = sv
		return nil
	}
	boolean := func(key string, dst *bool) error {
		v, ok := table[key]
		if !ok {
			return nil
		}
		bv, ok := v.(bool)
		if !ok {
			return fmt.Errorf("%s: %s must be a boolean, got %T", label, key, v)
		}
		*dst = bv
		return nil
	}
	strs := func(key string, dst *[]string) error {
		v, ok := table[key]
		if !ok {
			return nil
		}
		list, err := stringSlice(v)
		if err != nil {
			return fmt.Errorf("%s: %s %v", label, key, err)
		}
		*dst = list
		return nil
	}

	spec := RuleSpec{Enabled: true}
	// Every decode step runs; their faults are joined, so a rule with several
	// wrong-typed keys reports all of them in one load.
	if err := errors.Join(
		str("name", &spec.Name), str("check", &spec.Check), str("subject", &spec.Subject),
		boolean("enabled", &spec.Enabled), boolean("ignoreCase", &spec.IgnoreCase),
		strs("scope", &spec.Scope), strs("include", &spec.Include), strs("exclude", &spec.Exclude),
	); err != nil {
		return RuleSpec{}, err
	}
	if spec.Name == "" {
		return RuleSpec{}, fmt.Errorf("%s: name is required", label)
	}
	if strings.HasPrefix(spec.Name, DefaultRulePrefix) {
		return RuleSpec{}, fmt.Errorf("%s: the %q name prefix is reserved for synthesized default rules", label, DefaultRulePrefix)
	}
	if spec.Check == "" {
		return RuleSpec{}, fmt.Errorf("%s: check is required", label)
	}

	// [rule.params] declares ONE parameter set; the repeated [[rule.params]]
	// form declares several, batched exactly like the legacy keywordArguments
	// list. Values keep the shapes the checks' Bind type-checks: string, bool,
	// integer, list of strings. Anything else fails the load here.
	if paramsRaw, present := table["params"]; present {
		var paramsTables []map[string]interface{}
		switch pv := paramsRaw.(type) {
		case map[string]interface{}:
			paramsTables = []map[string]interface{}{pv}
		case []map[string]interface{}:
			paramsTables = pv
		case []interface{}:
			// The inline form params = [{...}, {...}] - the direct spelling of
			// the legacy keywordArguments list - decodes as []interface{}.
			paramsTables = make([]map[string]interface{}, 0, len(pv))
			for _, item := range pv {
				m, ok := item.(map[string]interface{})
				if !ok {
					return RuleSpec{}, fmt.Errorf("%s: params must be a table or an array of tables, got a %T element", label, item)
				}
				paramsTables = append(paramsTables, m)
			}
		default:
			return RuleSpec{}, fmt.Errorf("%s: params must be a table or an array of tables, got %T", label, paramsRaw)
		}
		spec.Params = make([]map[string]interface{}, 0, len(paramsTables))
		for _, paramsTable := range paramsTables {
			set := make(map[string]interface{}, len(paramsTable))
			for key, v := range paramsTable {
				switch val := v.(type) {
				case string, bool, int64:
					set[key] = val
				case []interface{}:
					list, err := stringSlice(val)
					if err != nil {
						return RuleSpec{}, fmt.Errorf("%s: params.%s %v", label, key, err)
					}
					set[key] = list
				default:
					return RuleSpec{}, fmt.Errorf("%s: params.%s has unsupported type %T", label, key, v)
				}
			}
			spec.Params = append(spec.Params, set)
		}
	}
	return spec, nil
}

// stringSlice validates every element, so a typo like include = [1] fails fast
// instead of yielding a silently empty filter.
func stringSlice(v interface{}) ([]string, error) {
	av, ok := v.([]interface{})
	if !ok {
		return nil, fmt.Errorf("must be an array of strings, got %T", v)
	}
	result := make([]string, 0, len(av))
	for _, item := range av {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("must be an array of strings, got a %T element", item)
		}
		result = append(result, s)
	}
	return result, nil
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

// LoadConfig loads the configuration from a TOML file. ParseConfig owns every
// check the load performs, so both entry points accept and reject exactly the
// same configs; this wrapper only names the file in the error.
func LoadConfig(file string) (*Config, error) {
	config, err := ParseConfig(file)
	if err != nil {
		return nil, fmt.Errorf("failed to parse config file '%s': %w", file, err)
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
