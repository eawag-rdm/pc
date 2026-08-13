package config

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func createTempConfigFile(t *testing.T, content string) string {
	tmpfile, err := os.CreateTemp("", "config-*.toml")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := tmpfile.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tmpfile.Close(); err != nil {
		t.Fatal(err)
	}

	return tmpfile.Name()
}

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name        string
		configData  string
		expectPanic bool
	}{
		{
			name: "ValidConfig",
			configData: `
				[operation]
				collector = "collector1"

				[test.test1]
				blacklist = ["item1", "item2"]

				[collector.collector1.attrs]
				attr1 = "value1"
			`,
			expectPanic: false,
		},
		{
			name: "InvalidConfigBothLists",
			configData: `
				[test.test1]
				blacklist = ["item1"]
				whitelist = ["item2"]
			`,
			expectPanic: true,
		},
		{
			name: "InvalidConfigNoLists",
			configData: `
				[test.test1]
			`,
			expectPanic: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configFile := createTempConfigFile(t, tt.configData)
			defer os.Remove(configFile)

			config, err := LoadConfig(configFile)
			if tt.expectPanic {
				assert.Error(t, err)
				assert.Nil(t, config)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, config)
			}
		})
	}
}

func TestConfigFile(t *testing.T) {
	// Read the config file in testdata
	cfg, err := ParseConfig("../../testdata/test_config.toml")
	if err != nil {
		t.Fatal(err)
	}
	// Check if the config file is loaded correctly: the checks are configured
	// as [[rule]] sections since the rules migration.
	assert.Equal(t, 0, len(cfg.Tests))
	assert.Equal(t, 5, len(cfg.Rules))
	assert.Equal(t, 2, len(cfg.Collectors))

	rule := func(name string) RuleSpec {
		for _, r := range cfg.Rules {
			if r.Name == name {
				return r
			}
		}
		t.Fatalf("no rule %q", name)
		return RuleSpec{}
	}

	// The secret-scan rule is disabled and carries its knobs as ONE typed
	// parameter set.
	secrets := rule("secret-scan")
	assert.Equal(t, "IsFreeOfSecrets", secrets.Check)
	assert.False(t, secrets.Enabled)
	assert.Equal(t, 1, len(secrets.Params))
	assert.Equal(t, "betterleaks", secrets.Params[0]["binary"])
	assert.Equal(t, int64(120), secrets.Params[0]["timeoutSeconds"])

	// The keyword rule carries its three [[rule.params]] groups on one rule.
	keywords := rule("sensitive-content")
	assert.Equal(t, "IsFreeOfKeywords", keywords.Check)
	assert.Equal(t, []string{"b"}, keywords.Include)
	assert.Equal(t, 3, len(keywords.Params))
	paths, ok := keywords.Params[2]["keywords"].([]string)
	assert.True(t, ok)
	assert.Equal(t, 1, len(paths))
	assert.Contains(t, paths, "/Users/")
}

func TestParseConfig(t *testing.T) {
	// Create a temporary TOML file for testing
	tomlContent := `
	[operation.main]
	collector = "collector1"

	[test.test1]
	blacklist = ["item1", "item2"]
	keywordArguments = [{ "arg1" = "value1" }, {"arg1" = "value1", "arg2" = ["value2", "value3"] }]

	[test.test3]
	whitelist = ["item3"]

	[test.test2]
	keywordArguments = [{"arg1" = "value1", "arg2" = ["/path/", "C:/path/"] }]

	[collector.collector1]
	attrs = { "key1" = "value1", "key2" = ["value2", "value3"] }
	`
	tmpFile, err := os.CreateTemp("", "test_config_*.toml")
	assert.NoError(t, err)
	defer os.Remove(tmpFile.Name())

	_, err = tmpFile.Write([]byte(tomlContent))
	assert.NoError(t, err)
	tmpFile.Close()

	// Parse the temporary TOML file
	config, err := ParseConfig(tmpFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	assert.NoError(t, err)
	assert.NotNil(t, config)

	// Validate the parsed data
	testConfig, ok := config.Tests["test1"]
	assert.True(t, ok)
	assert.ElementsMatch(t, []string{"item1", "item2"}, testConfig.Blacklist)
	assert.Len(t, testConfig.KeywordArguments, 2)
	assert.ElementsMatch(t, []string{"value2", "value3"}, testConfig.KeywordArguments[1]["arg2"])
	assert.Equal(t, "item1", testConfig.Blacklist[0])
	assert.Equal(t, "value1", testConfig.KeywordArguments[0]["arg1"])

	testConfig3, ok := config.Tests["test3"]
	assert.True(t, ok)
	assert.ElementsMatch(t, []string{"item3"}, testConfig3.Whitelist)

	testConfig2, ok := config.Tests["test2"]
	assert.True(t, ok)
	assert.Len(t, testConfig2.KeywordArguments, 1)
	assert.Equal(t, "value1", testConfig2.KeywordArguments[0]["arg1"])
	assert.ElementsMatch(t, []string{"/path/", "C:/path/"}, testConfig2.KeywordArguments[0]["arg2"])
	assert.Equal(t, 2, len(testConfig2.KeywordArguments[0]["arg2"].([]string)))

	collectorConfig, ok := config.Collectors["collector1"]
	assert.True(t, ok)
	assert.Equal(t, "value1", collectorConfig.Attrs["key1"])
	assert.ElementsMatch(t, []string{"value2", "value3"}, collectorConfig.Attrs["key2"])

	operationConfig, ok := config.Operation["main"]
	assert.True(t, ok)
	assert.Equal(t, "collector1", operationConfig.Collector)

}

func TestAssesLists(t *testing.T) {
	tests := []struct {
		blacklist []string
		whitelist []string
		expectErr bool
	}{
		{[]string{"item1"}, []string{}, false},
		{[]string{}, []string{"item1"}, false},
		{[]string{}, []string{}, false},
		{[]string{"item1"}, []string{"item2"}, true},
		{[]string{"item1"}, []string{"item1"}, true},
	}

	for _, tt := range tests {
		err := assesLists(tt.blacklist, tt.whitelist)
		if tt.expectErr {
			assert.Error(t, err)
		} else {
			assert.NoError(t, err)
		}
	}
}

func TestParseSummaryIntroText(t *testing.T) {
	t.Run("CustomIntroText", func(t *testing.T) {
		tomlContent := `
		[general]
		summaryIntroText = "Custom intro text for testing."
		`
		configFile := createTempConfigFile(t, tomlContent)
		defer os.Remove(configFile)

		config, err := ParseConfig(configFile)
		assert.NoError(t, err)
		assert.Equal(t, "Custom intro text for testing.", config.General.SummaryIntroText)
	})

	t.Run("EmptyIntroText", func(t *testing.T) {
		tomlContent := `
		[general]
		summaryIntroText = ""
		`
		configFile := createTempConfigFile(t, tomlContent)
		defer os.Remove(configFile)

		config, err := ParseConfig(configFile)
		assert.NoError(t, err)
		assert.Equal(t, "", config.General.SummaryIntroText)
	})

	t.Run("DefaultIntroText", func(t *testing.T) {
		tomlContent := `
		[general]
		maxArchiveFileSize = 1000
		`
		configFile := createTempConfigFile(t, tomlContent)
		defer os.Remove(configFile)

		config, err := ParseConfig(configFile)
		assert.NoError(t, err)
		assert.Equal(t, DefaultSummaryIntroText, config.General.SummaryIntroText)
	})

	t.Run("NoGeneralSection", func(t *testing.T) {
		tomlContent := `
		[test.test1]
		blacklist = []
		`
		configFile := createTempConfigFile(t, tomlContent)
		defer os.Remove(configFile)

		config, err := ParseConfig(configFile)
		assert.NoError(t, err)
		assert.Equal(t, DefaultSummaryIntroText, config.General.SummaryIntroText)
	})
}

func TestParseSummaryTruncationSettings(t *testing.T) {
	t.Run("CustomValues", func(t *testing.T) {
		tomlContent := `
		[general]
		summaryMaxIssuesBeforeTruncation = 10
		summaryMinGroupSizeForTruncation = 5
		`
		configFile := createTempConfigFile(t, tomlContent)
		defer os.Remove(configFile)

		config, err := ParseConfig(configFile)
		assert.NoError(t, err)
		assert.Equal(t, 10, config.General.SummaryMaxIssuesBeforeTruncation)
		assert.Equal(t, 5, config.General.SummaryMinGroupSizeForTruncation)
	})

	t.Run("DefaultValues", func(t *testing.T) {
		tomlContent := `
		[general]
		maxArchiveFileSize = 1000
		`
		configFile := createTempConfigFile(t, tomlContent)
		defer os.Remove(configFile)

		config, err := ParseConfig(configFile)
		assert.NoError(t, err)
		assert.Equal(t, DefaultSummaryMaxIssuesBeforeTruncation, config.General.SummaryMaxIssuesBeforeTruncation)
		assert.Equal(t, DefaultSummaryMinGroupSizeForTruncation, config.General.SummaryMinGroupSizeForTruncation)
	})

	t.Run("NoGeneralSection", func(t *testing.T) {
		tomlContent := `
		[test.test1]
		blacklist = []
		`
		configFile := createTempConfigFile(t, tomlContent)
		defer os.Remove(configFile)

		config, err := ParseConfig(configFile)
		assert.NoError(t, err)
		assert.Equal(t, DefaultSummaryMaxIssuesBeforeTruncation, config.General.SummaryMaxIssuesBeforeTruncation)
		assert.Equal(t, DefaultSummaryMinGroupSizeForTruncation, config.General.SummaryMinGroupSizeForTruncation)
	})
}

func TestParseServerConfig(t *testing.T) {
	t.Run("DefaultsWhenAbsent", func(t *testing.T) {
		tomlContent := `
		[test.test1]
		blacklist = []
		`
		configFile := createTempConfigFile(t, tomlContent)
		defer os.Remove(configFile)

		config, err := ParseConfig(configFile)
		assert.NoError(t, err)
		assert.NotNil(t, config.Server)
		assert.Equal(t, DefaultServerTrustProxyHeaders, config.Server.TrustProxyHeaders)
		assert.Nil(t, config.Server.TrustedProxies)
		assert.Nil(t, config.Server.AllowedOrigins)
		assert.Equal(t, DefaultServerPerIPRequestsPerHour, config.Server.PerIPRequestsPerHour)
		assert.Equal(t, DefaultServerGlobalRequestsPerHour, config.Server.GlobalRequestsPerHour)
		assert.Equal(t, DefaultServerBurstFactor, config.Server.BurstFactor)
		assert.Equal(t, DefaultServerAnalysisBusyWaitSeconds, config.Server.AnalysisBusyWaitSeconds)
		assert.Equal(t, DefaultServerMaxTrackedRateKeys, config.Server.MaxTrackedRateKeys)
		assert.Equal(t, DefaultServerContactMessage, config.Server.ContactMessage)
		assert.Equal(t, DefaultServerLogClientIP, config.Server.LogClientIP)
		assert.Equal(t, DefaultServerRequestTimeoutSeconds, config.Server.RequestTimeoutSeconds)
	})

	t.Run("OverriddenWhenPresent", func(t *testing.T) {
		tomlContent := `
		[server]
		trustProxyHeaders  = false
		trustedProxies     = ["10.0.0.0/8", "192.168.0.0/16"]
		allowedOrigins     = ["https://a.example.org", "https://b.example.org"]
		perIPRequestsPerHour  = 7
		globalRequestsPerHour = 42
		burstFactor           = 0.25
		analysisBusyWaitSeconds = 9
		maxTrackedRateKeys    = 500
		contactMessage        = "Contact ops."
		logClientIP           = false
		requestTimeoutSeconds = 120
		`
		configFile := createTempConfigFile(t, tomlContent)
		defer os.Remove(configFile)

		config, err := ParseConfig(configFile)
		assert.NoError(t, err)
		assert.False(t, config.Server.TrustProxyHeaders)
		assert.Equal(t, []string{"10.0.0.0/8", "192.168.0.0/16"}, config.Server.TrustedProxies)
		assert.Equal(t, []string{"https://a.example.org", "https://b.example.org"}, config.Server.AllowedOrigins)
		assert.Equal(t, 7, config.Server.PerIPRequestsPerHour)
		assert.Equal(t, 42, config.Server.GlobalRequestsPerHour)
		assert.Equal(t, 0.25, config.Server.BurstFactor)
		assert.Equal(t, 9, config.Server.AnalysisBusyWaitSeconds)
		assert.Equal(t, 500, config.Server.MaxTrackedRateKeys)
		assert.Equal(t, "Contact ops.", config.Server.ContactMessage)
		assert.False(t, config.Server.LogClientIP)
		assert.Equal(t, 120, config.Server.RequestTimeoutSeconds)
	})

	t.Run("BurstFactorAsFloat", func(t *testing.T) {
		tomlContent := `
		[server]
		burstFactor = 1.5
		`
		configFile := createTempConfigFile(t, tomlContent)
		defer os.Remove(configFile)

		config, err := ParseConfig(configFile)
		assert.NoError(t, err)
		assert.Equal(t, 1.5, config.Server.BurstFactor)
	})

	t.Run("BurstFactorAsInt", func(t *testing.T) {
		tomlContent := `
		[server]
		burstFactor = 2
		`
		configFile := createTempConfigFile(t, tomlContent)
		defer os.Remove(configFile)

		config, err := ParseConfig(configFile)
		assert.NoError(t, err)
		assert.Equal(t, 2.0, config.Server.BurstFactor)
	})

	t.Run("SMTPDefaultsWhenAbsent", func(t *testing.T) {
		tomlContent := `
		[test.test1]
		blacklist = []
		`
		configFile := createTempConfigFile(t, tomlContent)
		defer os.Remove(configFile)

		config, err := ParseConfig(configFile)
		assert.NoError(t, err)
		// SMTP defaults: port 25, no host/from/to (alerts disabled).
		assert.NotNil(t, config.Server.SMTP)
		assert.Equal(t, "", config.Server.SMTP.Host)
		assert.Equal(t, DefaultServerSMTPPort, config.Server.SMTP.Port)
		assert.Equal(t, "", config.Server.SMTP.From)
		assert.Nil(t, config.Server.SMTP.To)
	})

	t.Run("SMTPOverriddenWhenPresent", func(t *testing.T) {
		tomlContent := `
		[server.smtp]
		host = "smtp.example.org"
		port = 587
		from = "alerts@example.org"
		to   = ["admin1@example.org", "admin2@example.org"]
		`
		configFile := createTempConfigFile(t, tomlContent)
		defer os.Remove(configFile)

		config, err := ParseConfig(configFile)
		assert.NoError(t, err)
		assert.Equal(t, "smtp.example.org", config.Server.SMTP.Host)
		assert.Equal(t, 587, config.Server.SMTP.Port)
		assert.Equal(t, "alerts@example.org", config.Server.SMTP.From)
		assert.Equal(t, []string{"admin1@example.org", "admin2@example.org"}, config.Server.SMTP.To)
	})
}

func TestParseCollectorAttrTypes(t *testing.T) {
	t.Run("IntegerAttrKept", func(t *testing.T) {
		configFile := createTempConfigFile(t, `
		[collector.LocalCollector]
		attrs = {maxFolderDepth = 3, maxFileCount = 500, includeFolders = true}
		`)
		defer os.Remove(configFile)
		config, err := ParseConfig(configFile)
		assert.NoError(t, err)
		attrs := config.Collectors["LocalCollector"].Attrs
		assert.Equal(t, int64(3), attrs["maxFolderDepth"])
		assert.Equal(t, int64(500), attrs["maxFileCount"])
		assert.Equal(t, true, attrs["includeFolders"])
	})

	t.Run("UnsupportedTypeFailsFast", func(t *testing.T) {
		configFile := createTempConfigFile(t, `
		[collector.LocalCollector]
		attrs = {maxFolderDepth = 1.5}
		`)
		defer os.Remove(configFile)
		_, err := ParseConfig(configFile)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "maxFolderDepth")
	})
}

func TestParseMaxArchiveMemberCount(t *testing.T) {
	t.Run("Configured", func(t *testing.T) {
		configFile := createTempConfigFile(t, `
		[general]
		maxArchiveMemberCount = 250
		`)
		defer os.Remove(configFile)
		config, err := ParseConfig(configFile)
		assert.NoError(t, err)
		assert.Equal(t, 250, config.General.MaxArchiveMemberCount)
	})

	t.Run("MissingUsesDefault", func(t *testing.T) {
		configFile := createTempConfigFile(t, `
		[general]
		`)
		defer os.Remove(configFile)
		config, err := ParseConfig(configFile)
		assert.NoError(t, err)
		assert.Equal(t, DefaultMaxArchiveMemberCount, config.General.MaxArchiveMemberCount)
	})

	t.Run("WrongTypeFailsFast", func(t *testing.T) {
		configFile := createTempConfigFile(t, `
		[general]
		maxArchiveMemberCount = "many"
		`)
		defer os.Remove(configFile)
		_, err := ParseConfig(configFile)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "maxArchiveMemberCount")
	})

	t.Run("NegativeFailsFast", func(t *testing.T) {
		configFile := createTempConfigFile(t, `
		[general]
		maxArchiveMemberCount = -5
		`)
		defer os.Remove(configFile)
		_, err := ParseConfig(configFile)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "maxArchiveMemberCount")
	})
}

func TestParseMaxPDFPages(t *testing.T) {
	t.Run("Configured", func(t *testing.T) {
		configFile := createTempConfigFile(t, `
		[general]
		maxPDFPages = 42
		`)
		defer os.Remove(configFile)
		config, err := ParseConfig(configFile)
		assert.NoError(t, err)
		assert.Equal(t, 42, config.General.MaxPDFPages)
	})
	t.Run("MissingUsesDefault", func(t *testing.T) {
		configFile := createTempConfigFile(t, `
		[general]
		`)
		defer os.Remove(configFile)
		config, err := ParseConfig(configFile)
		assert.NoError(t, err)
		assert.Equal(t, DefaultMaxPDFPages, config.General.MaxPDFPages)
	})
	t.Run("NegativeFailsFast", func(t *testing.T) {
		configFile := createTempConfigFile(t, `
		[general]
		maxPDFPages = -1
		`)
		defer os.Remove(configFile)
		_, err := ParseConfig(configFile)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "maxPDFPages")
	})
	t.Run("EffectiveZeroDefaults", func(t *testing.T) {
		g := &GeneralConfig{}
		assert.Equal(t, DefaultMaxPDFPages, g.EffectiveMaxPDFPages())
		g.MaxPDFPages = 7
		assert.Equal(t, 7, g.EffectiveMaxPDFPages())
	})
}

func TestParseMaxPDFFileSize(t *testing.T) {
	t.Run("Configured", func(t *testing.T) {
		configFile := createTempConfigFile(t, `
		[general]
		maxPDFFileSize = 1234567
		`)
		defer os.Remove(configFile)
		config, err := ParseConfig(configFile)
		assert.NoError(t, err)
		assert.Equal(t, int64(1234567), config.General.MaxPDFFileSize)
	})
	t.Run("MissingUsesDefault", func(t *testing.T) {
		configFile := createTempConfigFile(t, `
		[general]
		`)
		defer os.Remove(configFile)
		config, err := ParseConfig(configFile)
		assert.NoError(t, err)
		assert.Equal(t, int64(DefaultMaxPDFFileSize), config.General.MaxPDFFileSize)
	})
	t.Run("NegativeFailsFast", func(t *testing.T) {
		configFile := createTempConfigFile(t, `
		[general]
		maxPDFFileSize = -1
		`)
		defer os.Remove(configFile)
		_, err := ParseConfig(configFile)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "maxPDFFileSize")
	})
	t.Run("WrongTypeFailsFast", func(t *testing.T) {
		configFile := createTempConfigFile(t, `
		[general]
		maxPDFFileSize = "5MB"
		`)
		defer os.Remove(configFile)
		_, err := ParseConfig(configFile)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "maxPDFFileSize")
	})
	t.Run("EffectiveZeroDefaults", func(t *testing.T) {
		g := &GeneralConfig{}
		assert.Equal(t, int64(DefaultMaxPDFFileSize), g.EffectiveMaxPDFFileSize())
		g.MaxPDFFileSize = 99
		assert.Equal(t, int64(99), g.EffectiveMaxPDFFileSize())
	})
}

// TestArchiveLimits verifies the single defaulting site for per-archive
// unpacking limits: non-positive values (e.g. from hand-built GeneralConfig
// structs that never pass ParseConfig) map to the documented defaults.
func TestArchiveLimits(t *testing.T) {
	t.Run("ZeroValuesDefault", func(t *testing.T) {
		g := &GeneralConfig{}
		ms, tm, mc := g.ArchiveLimits()
		assert.Equal(t, int64(DefaultMaxArchiveFileSize), ms)
		assert.Equal(t, int64(DefaultMaxTotalArchiveMemory), tm)
		assert.Equal(t, DefaultMaxArchiveMemberCount, mc)
	})

	t.Run("ConfiguredPassthrough", func(t *testing.T) {
		g := &GeneralConfig{
			MaxArchiveFileSize:    5 * 1024 * 1024,
			MaxTotalArchiveMemory: 50 * 1024 * 1024,
			MaxArchiveMemberCount: 42,
		}
		ms, tm, mc := g.ArchiveLimits()
		assert.Equal(t, int64(5*1024*1024), ms)
		assert.Equal(t, int64(50*1024*1024), tm)
		assert.Equal(t, 42, mc)
	})
}

// TestParseServerConfigWrongType verifies that a [server] / [server.smtp] key that
// is PRESENT but the wrong type fails fast (ParseConfig returns a non-nil error
// naming the key) instead of being silently skipped and keeping the default.
func TestParseServerConfigWrongType(t *testing.T) {
	tests := []struct {
		name      string
		toml      string
		errSubstr string // substring (the key name) the error must mention
	}{
		{
			name: "PerIPRequestsPerHourString",
			toml: `
			[server]
			perIPRequestsPerHour = "4"
			`,
			errSubstr: "perIPRequestsPerHour",
		},
		{
			name: "RequestTimeoutSecondsBool",
			toml: `
			[server]
			requestTimeoutSeconds = true
			`,
			errSubstr: "requestTimeoutSeconds",
		},
		{
			name: "ListenAddressInt",
			toml: `
			[server]
			listenAddress = 123
			`,
			errSubstr: "listenAddress",
		},
		{
			name: "BurstFactorString",
			toml: `
			[server]
			burstFactor = "x"
			`,
			errSubstr: "burstFactor",
		},
		{
			name: "TrustProxyHeadersString",
			toml: `
			[server]
			trustProxyHeaders = "yes"
			`,
			errSubstr: "trustProxyHeaders",
		},
		{
			name: "GlobalRequestsPerHourString",
			toml: `
			[server]
			globalRequestsPerHour = "20"
			`,
			errSubstr: "globalRequestsPerHour",
		},
		{
			name: "AnalysisBusyWaitSecondsString",
			toml: `
			[server]
			analysisBusyWaitSeconds = "2"
			`,
			errSubstr: "analysisBusyWaitSeconds",
		},
		{
			name: "MaxTrackedRateKeysFloat",
			toml: `
			[server]
			maxTrackedRateKeys = 1.5
			`,
			errSubstr: "maxTrackedRateKeys",
		},
		{
			name: "ContactMessageInt",
			toml: `
			[server]
			contactMessage = 7
			`,
			errSubstr: "contactMessage",
		},
		{
			name: "LogClientIPString",
			toml: `
			[server]
			logClientIP = "true"
			`,
			errSubstr: "logClientIP",
		},
		{
			name: "TrustedProxiesString",
			toml: `
			[server]
			trustedProxies = "10.0.0.0/8"
			`,
			errSubstr: "trustedProxies",
		},
		{
			name: "AllowedOriginsString",
			toml: `
			[server]
			allowedOrigins = "https://a.example.org"
			`,
			errSubstr: "allowedOrigins",
		},
		{
			name: "SMTPPortString",
			toml: `
			[server.smtp]
			port = "25"
			`,
			errSubstr: "port",
		},
		{
			name: "SMTPHostInt",
			toml: `
			[server.smtp]
			host = 25
			`,
			errSubstr: "host",
		},
		{
			name: "SMTPFromInt",
			toml: `
			[server.smtp]
			from = 1
			`,
			errSubstr: "from",
		},
		{
			name: "SMTPToString",
			toml: `
			[server.smtp]
			to = "admin@example.org"
			`,
			errSubstr: "to",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configFile := createTempConfigFile(t, tt.toml)
			defer os.Remove(configFile)

			// ParseConfig must reject the wrong-typed key.
			config, err := ParseConfig(configFile)
			assert.Error(t, err)
			assert.Nil(t, config)
			if err != nil {
				assert.Contains(t, err.Error(), tt.errSubstr)
			}

			// LoadConfig wraps ParseConfig; it must also fail.
			cfg, lerr := LoadConfig(configFile)
			assert.Error(t, lerr)
			assert.Nil(t, cfg)
		})
	}
}

// TestParseServerConfigSliceElementType verifies that a [server] / [server.smtp]
// slice key whose CONTAINER is a TOML array but whose ELEMENTS are not all strings
// fails fast (instead of silently dropping the non-string elements). It also
// confirms an all-string slice still parses and a missing key keeps the default.
func TestParseServerConfigSliceElementType(t *testing.T) {
	t.Run("TrustedProxiesNonStringElement", func(t *testing.T) {
		tomlContent := `
		[server]
		trustedProxies = [1, 2]
		`
		configFile := createTempConfigFile(t, tomlContent)
		defer os.Remove(configFile)

		config, err := ParseConfig(configFile)
		assert.Error(t, err)
		assert.Nil(t, config)
		if err != nil {
			assert.Contains(t, err.Error(), "trustedProxies")
		}

		cfg, lerr := LoadConfig(configFile)
		assert.Error(t, lerr)
		assert.Nil(t, cfg)
		if lerr != nil {
			assert.Contains(t, lerr.Error(), "trustedProxies")
		}
	})

	t.Run("AllowedOriginsMixedElement", func(t *testing.T) {
		tomlContent := `
		[server]
		allowedOrigins = ["ok", 5]
		`
		configFile := createTempConfigFile(t, tomlContent)
		defer os.Remove(configFile)

		config, err := ParseConfig(configFile)
		assert.Error(t, err)
		assert.Nil(t, config)
		if err != nil {
			assert.Contains(t, err.Error(), "allowedOrigins")
		}

		cfg, lerr := LoadConfig(configFile)
		assert.Error(t, lerr)
		assert.Nil(t, cfg)
		if lerr != nil {
			assert.Contains(t, lerr.Error(), "allowedOrigins")
		}
	})

	t.Run("SMTPToMixedElement", func(t *testing.T) {
		tomlContent := `
		[server.smtp]
		to = ["a@b.c", 7]
		`
		configFile := createTempConfigFile(t, tomlContent)
		defer os.Remove(configFile)

		config, err := ParseConfig(configFile)
		assert.Error(t, err)
		assert.Nil(t, config)
		if err != nil {
			assert.Contains(t, err.Error(), "to")
		}

		cfg, lerr := LoadConfig(configFile)
		assert.Error(t, lerr)
		assert.Nil(t, cfg)
		if lerr != nil {
			assert.Contains(t, lerr.Error(), "to")
		}
	})

	t.Run("AllStringSliceParses", func(t *testing.T) {
		tomlContent := `
		[server]
		trustedProxies = ["10.0.0.0/8", "192.168.0.0/16"]
		allowedOrigins = ["https://a.example.org"]

		[server.smtp]
		to = ["admin1@example.org", "admin2@example.org"]
		`
		configFile := createTempConfigFile(t, tomlContent)
		defer os.Remove(configFile)

		config, err := ParseConfig(configFile)
		assert.NoError(t, err)
		assert.NotNil(t, config)
		assert.Equal(t, []string{"10.0.0.0/8", "192.168.0.0/16"}, config.Server.TrustedProxies)
		assert.Equal(t, []string{"https://a.example.org"}, config.Server.AllowedOrigins)
		assert.Equal(t, []string{"admin1@example.org", "admin2@example.org"}, config.Server.SMTP.To)
	})

	t.Run("MissingSliceKeyKeepsDefault", func(t *testing.T) {
		tomlContent := `
		[server]
		listenAddress = "0.0.0.0:9090"
		`
		configFile := createTempConfigFile(t, tomlContent)
		defer os.Remove(configFile)

		config, err := ParseConfig(configFile)
		assert.NoError(t, err)
		assert.NotNil(t, config)
		assert.Nil(t, config.Server.TrustedProxies)
		assert.Nil(t, config.Server.AllowedOrigins)
		assert.Nil(t, config.Server.SMTP.To)
	})
}

// TestParseServerConfigBurstFactorAcceptsIntAndFloat asserts burstFactor accepts
// both a TOML float (0.5) and a bare int (1) without error.
func TestParseServerConfigBurstFactorAcceptsIntAndFloat(t *testing.T) {
	cases := []struct {
		name string
		toml string
		want float64
	}{
		{"Float", "[server]\nburstFactor = 0.5\n", 0.5},
		{"Int", "[server]\nburstFactor = 1\n", 1.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configFile := createTempConfigFile(t, tc.toml)
			defer os.Remove(configFile)

			config, err := ParseConfig(configFile)
			assert.NoError(t, err)
			assert.NotNil(t, config)
			assert.Equal(t, tc.want, config.Server.BurstFactor)
		})
	}
}

// TestParseServerConfigMissingKeyKeepsDefault confirms a MISSING [server] key
// still yields the built-in default with no error (only present-but-wrong-typed
// keys are rejected).
func TestParseServerConfigMissingKeyKeepsDefault(t *testing.T) {
	// [server] present but with only one key set; the rest must keep defaults.
	tomlContent := `
	[server]
	listenAddress = "0.0.0.0:9090"
	`
	configFile := createTempConfigFile(t, tomlContent)
	defer os.Remove(configFile)

	config, err := ParseConfig(configFile)
	assert.NoError(t, err)
	assert.NotNil(t, config)
	// Overridden key took effect.
	assert.Equal(t, "0.0.0.0:9090", config.Server.ListenAddress)
	// Missing keys keep their defaults.
	assert.Equal(t, DefaultServerPerIPRequestsPerHour, config.Server.PerIPRequestsPerHour)
	assert.Equal(t, DefaultServerBurstFactor, config.Server.BurstFactor)
	assert.Equal(t, DefaultServerRequestTimeoutSeconds, config.Server.RequestTimeoutSeconds)
	assert.Equal(t, DefaultServerSMTPPort, config.Server.SMTP.Port)
}

// TestParseShippedConfigsLoad confirms the shipped, correctly-typed configs
// still load cleanly. It goes through LoadConfig, the entry point both
// frontends use, so the whitelist/blacklist check ParseConfig now owns is
// exercised against the configs it is meant to gate.
func TestParseShippedConfigsLoad(t *testing.T) {
	for _, path := range []string{
		"../../pc.toml",
		"../../pc.toml.example",
		"../../testdata/test_config.toml",
	} {
		t.Run(path, func(t *testing.T) {
			if _, err := os.Stat(path); os.IsNotExist(err) {
				t.Skipf("%s is not in the tree (pc.toml is a local, gitignored config)", path)
			}
			cfg, err := LoadConfig(path)
			assert.NoError(t, err)
			assert.NotNil(t, cfg)
		})
	}
}

// TestRuleMixedWithTestSection pins the no-merge rule: a [[rule]] and a
// [test.X] section for the SAME check is a load error, never a merge.
func TestRuleMixedWithTestSection(t *testing.T) {
	_, err := loadTOML(t, `
[test.HasOnlyASCII]
blacklist = []

[[rule]]
name  = "ascii"
check = "HasOnlyASCII"
`)
	if err == nil {
		t.Fatal("a [[rule]] and a [test.X] for one check must refuse the load")
	}
	for _, want := range []string{"HasOnlyASCII", "one surface"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name %q: %v", want, err)
		}
	}
	// Different checks on different surfaces are fine.
	cfg, err := loadTOML(t, `
[test.HasOnlyASCII]
blacklist = []

[[rule]]
name  = "whitespace"
check = "HasNoWhiteSpace"
`)
	if err != nil {
		t.Fatalf("distinct checks may use distinct surfaces: %v", err)
	}
	if len(cfg.Rules) != 1 || cfg.Rules[0].Name != "whitespace" {
		t.Fatalf("rule not decoded: %+v", cfg.Rules)
	}

	// Several collisions are reported at once, not one per load.
	_, err = loadTOML(t, `
[test.HasOnlyASCII]
blacklist = []

[test.HasNoWhiteSpace]
blacklist = []

[[rule]]
name  = "a"
check = "HasOnlyASCII"

[[rule]]
name  = "b"
check = "HasNoWhiteSpace"
`)
	if err == nil {
		t.Fatal("expected a load error")
	}
	for _, want := range []string{"HasOnlyASCII", "HasNoWhiteSpace"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("aggregated error must name the %q collision: %v", want, err)
		}
	}
}

// TestRuleSpecDecode pins the [[rule]] decode: defaults, typed values, and the
// fail-fast rejections (missing identity, unknown key, wrong-typed key,
// unsupported params value).
func TestRuleSpecDecode(t *testing.T) {
	cfg, err := loadTOML(t, `
[[rule]]
name       = "credentials"
check      = "IsFreeOfKeywords"
scope      = ["file", "archive-member"]
subject    = "path"
enabled    = false
ignoreCase = true
include    = ["\\.csv$"]
exclude    = ["^raw/"]
  [rule.params]
  keywords = ["password", "api_key"]
  info     = "Possible credentials"
  depth    = 3
  strict   = true

[[rule]]
name  = "defaults"
check = "HasOnlyASCII"
`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(cfg.Rules))
	}
	full := cfg.Rules[0]
	if full.Name != "credentials" || full.Check != "IsFreeOfKeywords" ||
		full.Subject != "path" || full.Enabled || !full.IgnoreCase || full.Legacy {
		t.Errorf("rule fields decoded wrong: %+v", full)
	}
	if len(full.Scope) != 2 || full.Scope[0] != "file" || full.Scope[1] != "archive-member" {
		t.Errorf("scope decoded wrong: %v", full.Scope)
	}
	if len(full.Include) != 1 || full.Include[0] != `\.csv$` || len(full.Exclude) != 1 {
		t.Errorf("selector lists decoded wrong: %v / %v", full.Include, full.Exclude)
	}
	if len(full.Params) != 1 {
		t.Fatalf("a single [rule.params] table must decode as ONE set: %v", full.Params)
	}
	set := full.Params[0]
	if kw, ok := set["keywords"].([]string); !ok || len(kw) != 2 {
		t.Errorf("string list param decoded wrong: %T %v", set["keywords"], set["keywords"])
	}
	if set["info"] != "Possible credentials" || set["depth"] != int64(3) || set["strict"] != true {
		t.Errorf("scalar params decoded wrong: %v", set)
	}
	minimal := cfg.Rules[1]
	if !minimal.Enabled || minimal.IgnoreCase || minimal.Subject != "" || minimal.Params != nil {
		t.Errorf("defaults decoded wrong: %+v", minimal)
	}

	rejected := []struct {
		name string
		doc  string
		want string
	}{
		{"missing name", "[[rule]]\ncheck = \"HasOnlyASCII\"\n", "name is required"},
		{"missing check", "[[rule]]\nname = \"x\"\n", "check is required"},
		{"reserved default prefix", "[[rule]]\nname = \"default:HasReadme\"\ncheck = \"HasOnlyASCII\"\n", "reserved for synthesized default rules"},
		{"unknown key", "[[rule]]\nname = \"x\"\ncheck = \"C\"\nincludes = []\n", `unknown key "includes"`},
		{"wrong-typed enabled", "[[rule]]\nname = \"x\"\ncheck = \"C\"\nenabled = \"yes\"\n", "must be a boolean"},
		{"wrong-typed include", "[[rule]]\nname = \"x\"\ncheck = \"C\"\ninclude = [1]\n", "array of strings"},
		{"nested params table", "[[rule]]\nname = \"x\"\ncheck = \"C\"\n[rule.params]\nnested = {a = 1}\n", "unsupported type"},
		{"mixed params list", "[[rule]]\nname = \"x\"\ncheck = \"C\"\n[rule.params]\nlist = [\"a\", 1]\n", "array of strings"},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadTOML(t, tc.doc)
			if err == nil {
				t.Fatal("expected a load error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error must name %q: %v", tc.want, err)
			}
		})
	}
}

// TestRuleSpecDecodeMultipleParamSets pins the repeated [[rule.params]] form:
// one rule, N parameter sets, in declaration order.
func TestRuleSpecDecodeMultipleParamSets(t *testing.T) {
	cfg, err := loadTOML(t, `
[[rule]]
name  = "multi"
check = "IsFreeOfKeywords"
  [[rule.params]]
  keywords = ["password"]
  info     = "credentials"
  [[rule.params]]
  keywords = ["Q:"]
  info     = "internal"
`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Rules) != 1 || len(cfg.Rules[0].Params) != 2 {
		t.Fatalf("expected one rule with two parameter sets: %+v", cfg.Rules)
	}
	if cfg.Rules[0].Params[0]["info"] != "credentials" || cfg.Rules[0].Params[1]["info"] != "internal" {
		t.Errorf("sets must keep declaration order: %v", cfg.Rules[0].Params)
	}
}

// TestParseConfigRejectsUnknownTopLevel pins the strict top-level surface: a
// typo like [[rules]] must refuse the load instead of silently discarding
// every declared rule and running on defaults.
func TestParseConfigRejectsUnknownTopLevel(t *testing.T) {
	_, err := loadTOML(t, "[[rules]]\nname = \"x\"\ncheck = \"HasOnlyASCII\"\n")
	if err == nil || !strings.Contains(err.Error(), "rules") {
		t.Fatalf("a [[rules]] typo must be a load error naming the key: %v", err)
	}
	_, err = loadTOML(t, "[generall]\nmaxPDFPages = 3\n")
	if err == nil || !strings.Contains(err.Error(), "generall") {
		t.Fatalf("an unknown top-level table must be a load error naming it: %v", err)
	}
}

// TestRuleDecodeAggregatesErrors pins that every faulty [[rule]] is reported
// in one load, not one per run - and that several faults WITHIN one rule are
// all reported too.
func TestRuleDecodeAggregatesErrors(t *testing.T) {
	_, err := loadTOML(t, `
[[rule]]
check = "HasOnlyASCII"

[[rule]]
name  = "typo"
check = "HasOnlyASCII"
includes = []
`)
	if err == nil {
		t.Fatal("expected a load error")
	}
	for _, want := range []string{"name is required", `unknown key "includes"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("aggregated error must name %q: %v", want, err)
		}
	}

	_, err = loadTOML(t, `
[[rule]]
name    = "bad-types"
check   = "HasOnlyASCII"
enabled = "yes"
include = [1]
`)
	if err == nil {
		t.Fatal("expected a load error")
	}
	for _, want := range []string{"must be a boolean", "array of strings"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("one rule's faults must all be reported: %q missing from %v", want, err)
		}
	}
}

// TestRuleSpecDecodeInlineParamsArray pins the inline array form - the direct
// spelling of the legacy keywordArguments list operators migrate from.
func TestRuleSpecDecodeInlineParamsArray(t *testing.T) {
	cfg, err := loadTOML(t, `
[[rule]]
name   = "multi-inline"
check  = "IsFreeOfKeywords"
params = [{keywords = ["password"], info = "credentials"}, {keywords = ["Q:"], info = "internal"}]
`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Rules) != 1 || len(cfg.Rules[0].Params) != 2 {
		t.Fatalf("expected one rule with two parameter sets: %+v", cfg.Rules)
	}
	if kw, ok := cfg.Rules[0].Params[1]["keywords"].([]string); !ok || len(kw) != 1 || kw[0] != "Q:" {
		t.Errorf("inline sets must decode like [[rule.params]]: %v", cfg.Rules[0].Params)
	}
	// A non-table element keeps a clear error.
	_, err = loadTOML(t, "[[rule]]\nname = \"x\"\ncheck = \"C\"\nparams = [1]\n")
	if err == nil || !strings.Contains(err.Error(), "array of tables") {
		t.Fatalf("a non-table params element must fail the load clearly: %v", err)
	}
}
