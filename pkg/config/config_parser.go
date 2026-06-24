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

type GeneralConfig struct {
	MaxArchiveFileSize               int64  // Maximum size for individual files in archives (bytes)
	MaxTotalArchiveMemory            int64  // Maximum total memory for archive processing (bytes)
	MaxContentScanFileSize           int64  // Maximum size for files that read content (like IsFreeOfKeywords) (bytes)
	SummaryIntroText                 string // Introductory text shown at the top of the summary
	SummaryMaxIssuesBeforeTruncation int    // Number of issues to show before truncating
	SummaryMinGroupSizeForTruncation int    // Minimum group size to trigger truncation
}

// Default values for the [server] section.
const (
	DefaultServerPerIPRequestsPerHour    = 4
	DefaultServerGlobalRequestsPerHour   = 20
	DefaultServerBurstFactor             = 0.5
	DefaultServerAnalysisBusyWaitSeconds = 2
	DefaultServerMaxTrackedRateKeys      = 10000
	DefaultServerContactMessage          = "If you can't resolve this yourself, please contact rdm@eawag.ch."
	DefaultServerLogClientIP             = true
	DefaultServerTrustProxyHeaders       = true
	DefaultServerRequestTimeoutSeconds   = 300
)

// ServerConfig holds the configuration for the HTTP server (the [server] section).
type ServerConfig struct {
	TrustProxyHeaders       bool     // Whether to trust proxy-set client IP headers
	TrustedProxies          []string // CIDRs allowed to set X-Real-IP
	AllowedOrigins          []string // CORS allow-list of origin URLs
	PerIPRequestsPerHour    int      // Per-IP hourly request budget
	GlobalRequestsPerHour   int      // Global hourly request budget
	BurstFactor             float64  // Additional headroom factor applied to budgets
	AnalysisBusyWaitSeconds int      // Seconds a busy request waits for the analysis gate before 503
	MaxTrackedRateKeys      int      // Limiter memory bound (max tracked rate keys)
	ContactMessage          string   // Contact suffix shown in error envelopes
	LogClientIP             bool     // Whether to log the client IP
	RequestTimeoutSeconds   int      // Hard upper bound for a request, in seconds
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
			MaxArchiveFileSize:               10 * 1024 * 1024,   // 10MB default
			MaxTotalArchiveMemory:            100 * 1024 * 1024,  // 100MB default
			MaxContentScanFileSize:           1024 * 1024 * 1024, // 1GB default for content scanning
			SummaryIntroText:                 DefaultSummaryIntroText,
			SummaryMaxIssuesBeforeTruncation: DefaultSummaryMaxIssuesBeforeTruncation,
			SummaryMinGroupSizeForTruncation: DefaultSummaryMinGroupSizeForTruncation,
		},
		Server: &ServerConfig{
			TrustProxyHeaders:       DefaultServerTrustProxyHeaders,
			TrustedProxies:          nil,
			AllowedOrigins:          nil,
			PerIPRequestsPerHour:    DefaultServerPerIPRequestsPerHour,
			GlobalRequestsPerHour:   DefaultServerGlobalRequestsPerHour,
			BurstFactor:             DefaultServerBurstFactor,
			AnalysisBusyWaitSeconds: DefaultServerAnalysisBusyWaitSeconds,
			MaxTrackedRateKeys:      DefaultServerMaxTrackedRateKeys,
			ContactMessage:          DefaultServerContactMessage,
			LogClientIP:             DefaultServerLogClientIP,
			RequestTimeoutSeconds:   DefaultServerRequestTimeoutSeconds,
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

	// Parse server section
	if serverData, ok := raw["server"].(map[string]interface{}); ok {
		if trustProxyHeaders, ok := serverData["trustProxyHeaders"].(bool); ok {
			c.Server.TrustProxyHeaders = trustProxyHeaders
		}
		if trustedProxies, ok := serverData["trustedProxies"].([]interface{}); ok {
			c.Server.TrustedProxies = parseStringSlice(trustedProxies)
		}
		if allowedOrigins, ok := serverData["allowedOrigins"].([]interface{}); ok {
			c.Server.AllowedOrigins = parseStringSlice(allowedOrigins)
		}
		if val, ok := serverData["perIPRequestsPerHour"].(int64); ok {
			c.Server.PerIPRequestsPerHour = int(val)
		}
		if val, ok := serverData["globalRequestsPerHour"].(int64); ok {
			c.Server.GlobalRequestsPerHour = int(val)
		}
		// burstFactor may be expressed as a TOML float (0.5) or a bare int (1, 2).
		switch val := serverData["burstFactor"].(type) {
		case float64:
			c.Server.BurstFactor = val
		case int64:
			c.Server.BurstFactor = float64(val)
		}
		if val, ok := serverData["analysisBusyWaitSeconds"].(int64); ok {
			c.Server.AnalysisBusyWaitSeconds = int(val)
		}
		if val, ok := serverData["maxTrackedRateKeys"].(int64); ok {
			c.Server.MaxTrackedRateKeys = int(val)
		}
		if contactMessage, ok := serverData["contactMessage"].(string); ok {
			c.Server.ContactMessage = contactMessage
		}
		if logClientIP, ok := serverData["logClientIP"].(bool); ok {
			c.Server.LogClientIP = logClientIP
		}
		if val, ok := serverData["requestTimeoutSeconds"].(int64); ok {
			c.Server.RequestTimeoutSeconds = int(val)
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
						case []interface{}:
							cc.Attrs[k] = parseStringSlice(val)
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

// check fore the default configurtion file 1. ~/.config/pc/config.toml 2. ./config.toml if exists return the path
func FindConfigFile() string {
	// check for the default configuration file
	// 1. ~/.config/pc/config.toml
	// 2. ./config.toml
	paths := []string{
		"~/pc.toml",
		"./pc.toml",
		"~/.config/pc.toml",
	}

	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""

}
