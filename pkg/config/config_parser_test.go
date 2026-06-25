package config

import (
	"os"
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
	// Check if the config file is loaded correctly
	assert.Equal(t, 3, len(cfg.Tests))
	assert.Equal(t, 2, len(cfg.Collectors))

	keywords, ok := (*cfg.Tests["IsFreeOfKeywords"]).KeywordArguments[2]["keywords"].([]string)
	assert.True(t, ok)
	assert.Equal(t, 1, len(keywords))
	assert.Contains(t, keywords, "/Users/")

}

func TestParseConfig(t *testing.T) {
	// Create a temporary TOML file for testing
	tomlContent := `
	[operation.main]
	collector = "collector1"

	[test.test1]
	blacklist = ["item1", "item2"]
	whitelist = ["item3"]
	keywordArguments = [{ "arg1" = "value1" }, {"arg1" = "value1", "arg2" = ["value2", "value3"] }]

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
	assert.ElementsMatch(t, []string{"item3"}, testConfig.Whitelist)
	assert.Len(t, testConfig.KeywordArguments, 2)
	assert.ElementsMatch(t, []string{"value2", "value3"}, testConfig.KeywordArguments[1]["arg2"])
	assert.Equal(t, "item1", testConfig.Blacklist[0])
	assert.Equal(t, "value1", testConfig.KeywordArguments[0]["arg1"])

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

// TestParseShippedConfigsLoad confirms the shipped, correctly-typed configs still
// parse cleanly after the fail-fast change.
func TestParseShippedConfigsLoad(t *testing.T) {
	for _, path := range []string{
		"../../pc.toml.example",
		"../../testdata/test_config.toml",
	} {
		t.Run(path, func(t *testing.T) {
			cfg, err := ParseConfig(path)
			assert.NoError(t, err)
			assert.NotNil(t, cfg)
		})
	}
}
