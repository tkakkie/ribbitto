package postgres

import (
	"net/url"
	"strings"
	"testing"
)

func TestPoolTimeoutConfig(t *testing.T) {
	t.Setenv("PGOPTIONS", "")
	for _, tt := range []struct {
		name            string
		query           url.Values
		options         string
		optionOverrides bool
	}{
		{name: "defaults"},
		{name: "disabled statement timeout", query: url.Values{"statement_timeout": {"0"}}},
		{name: "explicit default", query: url.Values{"statement_timeout": {"30s"}}},
		{name: "explicit marker spelling", query: url.Values{"statement_timeout": {"30000ms"}}},
		{name: "query values", query: url.Values{
			"statement_timeout": {"250ms"}, "lock_timeout": {"0"},
			"idle_in_transaction_session_timeout": {"2min"},
		}},
		{name: "separate options", options: "-c statement_timeout=250ms -c lock_timeout=0 -c idle_in_transaction_session_timeout=2min", optionOverrides: true},
		{name: "joined options", options: "-cstatement_timeout=250ms -clock_timeout=0 -cidle_in_transaction_session_timeout=2min", optionOverrides: true},
		{name: "long options", options: "--statement-timeout=250ms --lock_timeout=0 --idle-in-transaction-session-timeout=2min", optionOverrides: true},
		{name: "unrelated escaped option", options: `-c application_name=contains\ -c\ statement_timeout=250ms`},
		{name: "partial override", query: url.Values{"lock_timeout": {"100ms"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			q := tt.query
			if q == nil {
				q = make(url.Values)
			}
			if tt.options != "" {
				q.Set("options", tt.options)
			}
			// pgx decodes URI escapes, but preserves literal '+' characters.
			config, err := parsePoolConfig("postgres://localhost/test?" + strings.ReplaceAll(q.Encode(), "+", "%20"))
			if err != nil {
				t.Fatal(err)
			}
			params := config.ConnConfig.RuntimeParams
			if params["options"] != tt.options {
				t.Fatalf("options = %q, want %q", params["options"], tt.options)
			}
			for name, fallback := range map[string]string{
				"statement_timeout": "30000ms", "lock_timeout": "5s",
				"idle_in_transaction_session_timeout": "60s",
			} {
				want := fallback
				if tt.optionOverrides {
					// No direct GUC may shadow the explicit startup option.
					if _, present := params[name]; present {
						t.Fatalf("default %s shadows options", name)
					}
					continue
				}
				if explicit, present := q[name]; present {
					want = explicit[0]
					if name == "statement_timeout" && want == defaultStatementTimeout {
						want = "30s"
					}
				}
				if params[name] != want {
					t.Errorf("%s = %q, want %q", name, params[name], want)
				}
			}
		})
	}
}
