package config

import "fmt"

// ValidateChecksConfig fails fast when a [test.*] section the checks dereference
// at scan time is missing or wrong-typed (nil-pointer on a missing section,
// failed type assertion on a wrong-typed key — a panic mid-scan otherwise; see
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
