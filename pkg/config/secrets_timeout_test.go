package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadTOML writes doc to a temp file and loads it through LoadConfig, the
// entry point both frontends use.
func loadTOML(t *testing.T, doc string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pc.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return LoadConfig(path)
}

// TestSecretsTimeoutWithinRequestBudget pins the one cross-section constraint
// the config layer kept when the per-check validation moved into the checks'
// own Bind: a secret-scan timeout longer than the server's request timeout is
// refused at load, even while the scan is disabled.
func TestSecretsTimeoutWithinRequestBudget(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		ok   bool
	}{
		{
			"rule params timeout over budget",
			"[server]\nrequestTimeoutSeconds = 60\n" +
				"[[rule]]\nname = \"secret-scan\"\ncheck = \"IsFreeOfSecrets\"\nenabled = false\n" +
				"  [rule.params]\n  timeoutSeconds = 120\n",
			false,
		},
		{
			"rule params timeout within budget",
			"[server]\nrequestTimeoutSeconds = 300\n" +
				"[[rule]]\nname = \"secret-scan\"\ncheck = \"IsFreeOfSecrets\"\nenabled = false\n" +
				"  [rule.params]\n  timeoutSeconds = 120\n",
			true,
		},
		{
			// The gate runs only when a secrets rule exists: with none there is
			// no timeout to hold against a tight budget.
			"no secrets configuration ignores the budget",
			"[server]\nrequestTimeoutSeconds = 60\n",
			true,
		},
		{
			// EVERY rule's timeout is validated, not just the last one's.
			"first of two rules over budget",
			"[server]\nrequestTimeoutSeconds = 60\n" +
				"[[rule]]\nname = \"scan-a\"\ncheck = \"IsFreeOfSecrets\"\nenabled = false\n" +
				"  [rule.params]\n  timeoutSeconds = 120\n" +
				"[[rule]]\nname = \"scan-b\"\ncheck = \"IsFreeOfSecrets\"\nenabled = false\n" +
				"  [rule.params]\n  timeoutSeconds = 30\n",
			false,
		},
		{
			// ...and EVERY parameter set's, not just the last set's.
			"first of two parameter sets over budget",
			"[server]\nrequestTimeoutSeconds = 60\n" +
				"[[rule]]\nname = \"scan\"\ncheck = \"IsFreeOfSecrets\"\nenabled = false\n" +
				"  [[rule.params]]\n  timeoutSeconds = 120\n" +
				"  [[rule.params]]\n  timeoutSeconds = 30\n",
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadTOML(t, tc.doc)
			if tc.ok && err != nil {
				t.Fatalf("expected the config to load, got: %v", err)
			}
			if !tc.ok {
				if err == nil {
					t.Fatal("expected the load to refuse the timeout combination")
				}
				if !strings.Contains(err.Error(), "timeoutSeconds") {
					t.Errorf("the error must name the setting: %v", err)
				}
			}
		})
	}
}
