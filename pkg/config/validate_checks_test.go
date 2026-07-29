package config

import (
	"strings"
	"testing"
)

// validChecksTestConfig returns the minimal Tests map ValidateChecksConfig must
// accept; tests mutate one aspect to assert rejection.
func validChecksTestConfig() *Config {
	return &Config{
		Tests: map[string]*TestConfig{
			"IsFreeOfKeywords": {KeywordArguments: []map[string]interface{}{
				{"keywords": []string{"password"}, "info": "Sensitive keyword found:"},
			}},
			"IsValidName": {KeywordArguments: []map[string]interface{}{
				{"disallowed_names": []string{".DS_Store"}},
			}},
			"HasReadme": {KeywordArguments: []map[string]interface{}{
				{"readme_names": []string{"readme.md", "readme.txt"}},
			}},
		},
	}
}

// TestValidateChecksConfig asserts the fail-fast contract: a [test.*] section or
// key the checks dereference at scan time that is missing or wrong-typed is
// rejected with an error naming the offending section/key - instead of surfacing
// as a request-time panic inside a worker goroutine (which would kill the
// process, not the request).
func TestValidateChecksConfig(t *testing.T) {
	t.Run("valid config passes", func(t *testing.T) {
		if err := ValidateChecksConfig(validChecksTestConfig()); err != nil {
			t.Fatalf("expected valid config, got: %v", err)
		}
	})

	t.Run("empty keywords list passes (check no-ops)", func(t *testing.T) {
		cfg := validChecksTestConfig()
		cfg.Tests["IsFreeOfKeywords"].KeywordArguments[0]["keywords"] = []string{}
		if err := ValidateChecksConfig(cfg); err != nil {
			t.Fatalf("empty keywords list must be allowed (harmless no-op), got: %v", err)
		}
	})

	t.Run("empty keywordArguments lists pass (checks no-op)", func(t *testing.T) {
		cfg := validChecksTestConfig()
		cfg.Tests["IsFreeOfKeywords"].KeywordArguments = nil
		cfg.Tests["IsValidName"].KeywordArguments = nil
		if err := ValidateChecksConfig(cfg); err != nil {
			t.Fatalf("empty keywordArguments must be allowed, got: %v", err)
		}
	})

	t.Run("nil config", func(t *testing.T) {
		if err := ValidateChecksConfig(nil); err == nil {
			t.Fatal("expected error for nil config")
		}
	})

	cases := []struct {
		name    string
		mutate  func(cfg *Config)
		wantSub string
	}{
		{"missing IsFreeOfKeywords section", func(cfg *Config) { delete(cfg.Tests, "IsFreeOfKeywords") }, "IsFreeOfKeywords"},
		{"nil IsFreeOfKeywords section", func(cfg *Config) { cfg.Tests["IsFreeOfKeywords"] = nil }, "IsFreeOfKeywords"},
		{"missing keywords key", func(cfg *Config) {
			delete(cfg.Tests["IsFreeOfKeywords"].KeywordArguments[0], "keywords")
		}, "keywords"},
		{"keywords wrong type (plain string)", func(cfg *Config) {
			cfg.Tests["IsFreeOfKeywords"].KeywordArguments[0]["keywords"] = "password"
		}, "keywords"},
		{"missing info key", func(cfg *Config) {
			delete(cfg.Tests["IsFreeOfKeywords"].KeywordArguments[0], "info")
		}, "info"},
		{"info wrong type", func(cfg *Config) {
			cfg.Tests["IsFreeOfKeywords"].KeywordArguments[0]["info"] = []string{"x"}
		}, "info"},
		{"missing IsValidName section", func(cfg *Config) { delete(cfg.Tests, "IsValidName") }, "IsValidName"},
		{"nil IsValidName section", func(cfg *Config) { cfg.Tests["IsValidName"] = nil }, "IsValidName"},
		{"missing disallowed_names key", func(cfg *Config) {
			delete(cfg.Tests["IsValidName"].KeywordArguments[0], "disallowed_names")
		}, "disallowed_names"},
		{"disallowed_names wrong type", func(cfg *Config) {
			cfg.Tests["IsValidName"].KeywordArguments[0]["disallowed_names"] = ".DS_Store"
		}, "disallowed_names"},
		{"missing HasReadme section", func(cfg *Config) { delete(cfg.Tests, "HasReadme") }, "HasReadme"},
		{"nil HasReadme section", func(cfg *Config) { cfg.Tests["HasReadme"] = nil }, "HasReadme"},
		{"HasReadme without keywordArguments", func(cfg *Config) {
			cfg.Tests["HasReadme"].KeywordArguments = nil
		}, "readme_names"},
		{"missing readme_names key", func(cfg *Config) {
			delete(cfg.Tests["HasReadme"].KeywordArguments[0], "readme_names")
		}, "readme_names"},
		{"empty readme_names list", func(cfg *Config) {
			cfg.Tests["HasReadme"].KeywordArguments[0]["readme_names"] = []string{}
		}, "readme_names"},
		{"readme_names wrong type", func(cfg *Config) {
			cfg.Tests["HasReadme"].KeywordArguments[0]["readme_names"] = "readme.md"
		}, "readme_names"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validChecksTestConfig()
			tc.mutate(cfg)
			err := ValidateChecksConfig(cfg)
			if err == nil {
				t.Fatalf("expected an error for %q", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not name %q", err.Error(), tc.wantSub)
			}
		})
	}
}
