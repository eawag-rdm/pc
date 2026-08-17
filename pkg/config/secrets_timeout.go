package config

import "fmt"

// DefaultSecretsTimeoutSeconds is the default IsFreeOfSecrets timeoutSeconds;
// the leak check reads it from here (config cannot import checks), so there is
// exactly one copy.
const DefaultSecretsTimeoutSeconds = 120

// secretsTimeoutWithinRequestBudget rejects a secret-scan timeout longer than
// the server's whole-request timeout: the scan cannot see the request deadline
// (check functions take no context), so it could keep the scanner running past
// the analysis deadline. The one cross-SECTION constraint the config layer
// keeps - everything per-check moved into the checks' own Bind (utils.Compile).
// It runs only when an IsFreeOfSecrets rule EXISTS - a config with no secrets
// configuration declares no timeout to validate - but then covers EVERY
// declared rule and every parameter set carrying a timeout, disabled rules
// included: a bad combination fails at load, not on the day the dormant scan
// is reactivated.
func secretsTimeoutWithinRequestBudget(c *Config) error {
	if c.Server == nil || c.Server.RequestTimeoutSeconds <= 0 {
		return nil
	}
	budget := int64(c.Server.RequestTimeoutSeconds)
	exceeds := func(timeout int64) error {
		if timeout > budget {
			return fmt.Errorf("IsFreeOfSecrets timeoutSeconds (%d) must not exceed [server] requestTimeoutSeconds (%d)", timeout, budget)
		}
		return nil
	}
	for _, rule := range c.Rules {
		if rule.Check != "IsFreeOfSecrets" {
			continue
		}
		declared := false
		for _, set := range rule.Params {
			if n, ok := set["timeoutSeconds"].(int64); ok {
				declared = true
				if err := exceeds(n); err != nil {
					return err
				}
			}
		}
		// A rule declaring no timeout runs the default, which must fit too.
		if !declared {
			if err := exceeds(int64(DefaultSecretsTimeoutSeconds)); err != nil {
				return err
			}
		}
	}
	return nil
}
