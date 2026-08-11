package config

import (
	"fmt"

	"github.com/eawag-rdm/pc/pkg/selector"
)

// DefaultSecretsTimeoutSeconds is the default [test.IsFreeOfSecrets]
// timeoutSeconds; the leak check reads it from here (config cannot import
// checks), so there is exactly one copy.
const DefaultSecretsTimeoutSeconds = 120

// ValidateChecksConfig fails fast when a [test.*] section the checks dereference
// at scan time is missing or wrong-typed (nil-pointer on a missing section,
// failed type assertion on a wrong-typed key - a panic mid-scan otherwise; see
// optimization.SafeRun for the runtime guard). Both the server (at boot) and
// the CLI (after config load) call this so the operator gets one clear,
// actionable error instead.
func ValidateChecksConfig(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}

	free, ok := cfg.Tests["IsFreeOfKeywords"]
	if !ok || free == nil {
		return fmt.Errorf("config section [test.IsFreeOfKeywords] is required (used by the keyword checks)")
	}
	for i, args := range free.KeywordArguments {
		if _, ok := args["keywords"].([]string); !ok {
			return fmt.Errorf("[test.IsFreeOfKeywords] keywordArguments entry %d: 'keywords' is required and must be a list of strings", i+1)
		}
		if _, ok := args["info"].(string); !ok {
			return fmt.Errorf("[test.IsFreeOfKeywords] keywordArguments entry %d: 'info' is required and must be a string", i+1)
		}
	}

	valid, ok := cfg.Tests["IsValidName"]
	if !ok || valid == nil {
		return fmt.Errorf("config section [test.IsValidName] is required (used by the file-name checks)")
	}
	for i, args := range valid.KeywordArguments {
		if _, ok := args["disallowed_names"].([]string); !ok {
			return fmt.Errorf("[test.IsValidName] keywordArguments entry %d: 'disallowed_names' is required and must be a list of strings", i+1)
		}
	}

	// [test.IsFreeOfSecrets] is optional (the check is disabled without it), but a
	// present attrs table must be well-typed so the scan doesn't misbehave at
	// runtime. Unknown attr keys fail fast to catch operator typos.
	if leaks, ok := cfg.Tests["IsFreeOfSecrets"]; ok && leaks != nil {
		// The lists are validated with the constructor the scan itself compiles
		// them with, so boot's verdict IS the scan's verdict: an uncompilable
		// pattern, an EMPTY entry and both lists set are refused here. Validated
		// while enabled = false too - a config the scan could not honour must
		// fail loudly at load, not on the day the dormant scan is reactivated.
		if _, err := selector.CompileLegacyRegexLists("IsFreeOfSecrets", "path", leaks.Whitelist, leaks.Blacklist); err != nil {
			return fmt.Errorf("[test.IsFreeOfSecrets]: unusable whitelist/blacklist: %v", err)
		}
		if leaks.Attrs != nil {
			for key, v := range leaks.Attrs {
				var typeOK bool
				switch key {
				case "enabled":
					_, typeOK = v.(bool)
				case "binary":
					s, isStr := v.(string)
					typeOK = isStr && s != ""
				case "timeoutSeconds", "maxProcs":
					n, isInt := v.(int64)
					typeOK = isInt && n > 0
				default:
					return fmt.Errorf("[test.IsFreeOfSecrets] attrs: unknown key '%s' (allowed: enabled, binary, timeoutSeconds, maxProcs)", key)
				}
				if !typeOK {
					return fmt.Errorf("[test.IsFreeOfSecrets] attrs: '%s' has the wrong type or an invalid value (%v)", key, v)
				}
			}
		}
		// The secret scan runs inside a server analysis request but cannot see its
		// deadline (check functions take no context), so a scan timeout longer than
		// the request timeout could keep the scanner running past the analysis
		// deadline. Reject that combination up front. Deliberately validated even
		// while enabled = false: a bad combination should fail at config load,
		// not on the day the dormant scan is reactivated.
		timeoutSeconds := int64(DefaultSecretsTimeoutSeconds)
		if leaks.Attrs != nil {
			if n, isInt := leaks.Attrs["timeoutSeconds"].(int64); isInt {
				timeoutSeconds = n
			}
		}
		if cfg.Server != nil && cfg.Server.RequestTimeoutSeconds > 0 && timeoutSeconds > int64(cfg.Server.RequestTimeoutSeconds) {
			return fmt.Errorf("[test.IsFreeOfSecrets] attrs: timeoutSeconds (%d) must not exceed [server] requestTimeoutSeconds (%d)", timeoutSeconds, cfg.Server.RequestTimeoutSeconds)
		}
	}

	// [test.HasReadme] configures which filenames count as a readme; the list
	// is shared by HasReadme and ReadMeContainsTOC.
	readme, ok := cfg.Tests["HasReadme"]
	if !ok || readme == nil {
		return fmt.Errorf("config section [test.HasReadme] is required (used by the readme checks)")
	}
	if len(readme.KeywordArguments) == 0 {
		return fmt.Errorf("[test.HasReadme] must define keywordArguments with 'readme_names' (a non-empty list of strings)")
	}
	for i, args := range readme.KeywordArguments {
		names, isList := args["readme_names"].([]string)
		if !isList || len(names) == 0 {
			return fmt.Errorf("[test.HasReadme] keywordArguments entry %d: 'readme_names' is required and must be a non-empty list of strings", i+1)
		}
	}

	return nil
}
