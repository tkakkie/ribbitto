package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// defaultStatementTimeout identifies the budget supplied by this package. Explicit
// URL values with this spelling are normalised below so they cannot opt in.
const defaultStatementTimeout = "30000ms"

// OpenPool connects the pgx pool that the sqlc queries use. Migrations keep
// using Open's database/sql handle, which goose needs. A nil counter installs
// no tracer at all, so the pool runs exactly as without metrics.
func OpenPool(ctx context.Context, url string, counter *QueryCounter) (*pgxpool.Pool, error) {
	config, err := parsePoolConfig(url)
	if err != nil {
		return nil, err
	}
	if counter != nil {
		config.ConnConfig.Tracer = counter
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("creating database pool: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connecting to database: %w", err)
	}
	return pool, nil
}

func parsePoolConfig(url string) (*pgxpool.Config, error) {
	if url == "" {
		return nil, fmt.Errorf("database URL is empty")
	}
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parsing database configuration: %w", err)
	}
	params := config.ConnConfig.RuntimeParams
	if params["statement_timeout"] == defaultStatementTimeout {
		params["statement_timeout"] = "30s"
	}
	options := optionNames(params["options"])
	for name := range params {
		options[strings.ToLower(name)] = true
	}
	for name, value := range map[string]string{
		// End slow queries before the HTTP write budget, even when
		// WriteTimeout leaves the request context live.
		"statement_timeout": defaultStatementTimeout,
		// Contended writes should fail well before the statement budget.
		"lock_timeout": "5s",
		// An abandoned transaction must not retain locks indefinitely, while
		// short gaps between statements (including seed hashing) remain safe.
		"idle_in_transaction_session_timeout": "60s",
	} {
		if !options[name] {
			params[name] = value
		}
	}
	return config, nil
}

// optionNames recognises PostgreSQL startup options, which pgx leaves intact
// rather than expanding into RuntimeParams. PostgreSQL splits on whitespace
// with backslash escaping, not shell quoting; -c accepts a separate or joined
// argument and -- accepts long GUC names (including hyphens for underscores).
func optionNames(options string) map[string]bool {
	var words []string
	var word strings.Builder
	escaped := false
	for _, c := range options {
		switch {
		case escaped:
			word.WriteRune(c)
			escaped = false
		case c == '\\':
			escaped = true
		case strings.ContainsRune(" \t\n\r\v\f", c):
			if word.Len() > 0 {
				words = append(words, word.String())
				word.Reset()
			}
		default:
			word.WriteRune(c)
		}
	}
	if word.Len() > 0 {
		words = append(words, word.String())
	}
	names := make(map[string]bool)
	for i := 0; i < len(words); i++ {
		arg := words[i]
		switch {
		case arg == "-c" && i+1 < len(words):
			i++
			arg = words[i]
		case strings.HasPrefix(arg, "-c"):
			arg = strings.TrimPrefix(arg, "-c")
		case strings.HasPrefix(arg, "--"):
			arg = strings.TrimPrefix(arg, "--")
		default:
			continue
		}
		name, _, found := strings.Cut(arg, "=")
		if found {
			names[strings.ToLower(strings.ReplaceAll(name, "-", "_"))] = true
		}
	}
	return names
}
