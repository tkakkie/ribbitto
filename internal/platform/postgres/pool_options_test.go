package postgres

import (
	"errors"
	"maps"
	"net/url"
	"strings"
	"testing"
)

func TestOptionNames(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   string
		want map[string]bool
	}{
		{name: "empty", in: "", want: map[string]bool{}},
		{name: "separate without equals", in: "-c statement_timeout", want: map[string]bool{}},
		{name: "trailing flag", in: "-c", want: map[string]bool{}},
		{name: "joined then trailing flag", in: "-clock_timeout=1 -c", want: map[string]bool{"lock_timeout": true}},
		{name: "mixed case separate", in: "-c Statement_Timeout=1s", want: map[string]bool{"statement_timeout": true}},
		{name: "uppercase hyphenated long option", in: "--STATEMENT-TIMEOUT=1", want: map[string]bool{"statement_timeout": true}},
		{name: "hyphenated after flag", in: "-c lock-timeout=1s", want: map[string]bool{"lock_timeout": true}},
		{name: "bare word", in: "statement_timeout=1s", want: map[string]bool{}},
		{name: "other flag", in: "-o foo=bar", want: map[string]bool{}},
		{name: "bare double dash", in: "--", want: map[string]bool{}},
		{name: "value containing equals", in: "-c application_name=x=y", want: map[string]bool{"application_name": true}},
		{
			name: "tab and newline separators",
			in:   "-c\tlock_timeout=1\n-c\nidle_in_transaction_session_timeout=2",
			want: map[string]bool{
				"lock_timeout":                        true,
				"idle_in_transaction_session_timeout": true,
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := optionNames(tt.in)
			if got == nil {
				t.Fatalf("optionNames(%q) = nil, want non-nil map", tt.in)
			}
			if !maps.Equal(got, tt.want) {
				t.Fatalf("optionNames(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParsePoolConfigErrors(t *testing.T) {
	t.Run("empty URL", func(t *testing.T) {
		t.Setenv("PGOPTIONS", "")
		config, err := parsePoolConfig("")
		if config != nil {
			t.Errorf("config = %v, want nil", config)
		}
		if err == nil || err.Error() != "database URL is empty" {
			t.Fatalf("err = %v, want %q", err, "database URL is empty")
		}
	})

	t.Run("invalid URL", func(t *testing.T) {
		t.Setenv("PGOPTIONS", "")
		config, err := parsePoolConfig("postgres://localhost:notaport/db")
		if config != nil {
			t.Errorf("config = %v, want nil", config)
		}
		const wantPrefix = "parsing database configuration: "
		if err == nil || !strings.HasPrefix(err.Error(), wantPrefix) {
			t.Fatalf("err = %v, want prefix %q", err, wantPrefix)
		}
		if errors.Unwrap(err) == nil {
			t.Fatalf("errors.Unwrap(%v) = nil, want wrapped error", err)
		}
	})
}

func TestParsePoolConfigOptionRows(t *testing.T) {
	for _, tt := range []struct {
		name      string
		pgoptions string
		query     url.Values
		options   string
		want      map[string]string
		absent    []string
	}{
		{
			name:  "mixed-case query parameter",
			query: url.Values{"Statement_Timeout": {"250ms"}},
			want: map[string]string{
				"Statement_Timeout":                   "250ms",
				"lock_timeout":                        "5s",
				"idle_in_transaction_session_timeout": "60s",
			},
			absent: []string{"statement_timeout"},
		},
		{
			name:    "single option in query",
			options: "-c lock_timeout=0",
			want: map[string]string{
				"options":                             "-c lock_timeout=0",
				"statement_timeout":                   "30000ms",
				"idle_in_transaction_session_timeout": "60s",
			},
			absent: []string{"lock_timeout"},
		},
		{
			name:      "single option in PGOPTIONS",
			pgoptions: "-c lock_timeout=0",
			want: map[string]string{
				"options":                             "-c lock_timeout=0",
				"statement_timeout":                   "30000ms",
				"idle_in_transaction_session_timeout": "60s",
			},
			absent: []string{"lock_timeout"},
		},
		{
			name:    "option and query parameter",
			query:   url.Values{"statement_timeout": {"250ms"}},
			options: "-c lock_timeout=0",
			want: map[string]string{
				"options":                             "-c lock_timeout=0",
				"statement_timeout":                   "250ms",
				"idle_in_transaction_session_timeout": "60s",
			},
			absent: []string{"lock_timeout"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PGOPTIONS", tt.pgoptions)
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
			for name, want := range tt.want {
				if got, present := params[name]; !present || got != want {
					t.Errorf("RuntimeParams[%q] = %q (present=%v), want %q", name, got, present, want)
				}
			}
			for _, name := range tt.absent {
				if got, present := params[name]; present {
					t.Errorf("RuntimeParams[%q] = %q, want absent", name, got)
				}
			}
		})
	}
}
