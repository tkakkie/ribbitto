// Package middleware supplies shared HTTP controls: security headers, the
// server-generated request ID and completion log, the session cookie and
// signed-in account, the request body limit, and rate limits on authentication
// forms, typing signals, reads, channel creation, branching, posting, pin changes
// and sidebar resyncs.
// LimitBody sees the server's real writer first, then RequestLog wraps
// CrossOriginProtection so even pre-routing rejections are logged.
// Request logs allow only ID, method, registered pattern, status and duration;
// streams log on completion. Request-scoped error logs and stream warnings
// explicitly include the same request_id from context; the once-only proxy
// notice and deduplicated i18n fallback warning do not. Unwrap preserves
// ResponseController operations. run configures RIBBITTO_LOG_FORMAT before
// constructors capture slog.Default(): json selects JSON on stderr; empty
// leaves it untouched; other values fail startup.
// Authentication limits count by client key: the peer address or, behind a
// trusted proxy, the rightmost untrusted X-Forwarded-For address; an IPv6
// client counts as its /64, and its /48 is charged too. Authentication limits
// warn once per instance when a loopback or private peer forwards addresses
// with no trusted proxies configured, without logging addresses. The
// independent typing, read, creation, branch, post, pin and resync limiters
// accept trusted organisation/member IDs from the authenticated web adapter.
// Typing and reads each share a budget across channels, topics, sessions and
// addresses; creation's budget is spent only by creating channels, from any
// session or address. Branch requests share a budget across channels, topics,
// sessions and addresses. Resyncs share a budget across all of a member's
// tabs and sessions. All limiters live in memory per process
// (docs/architecture/rate-limits.md); none accesses feature state or tables.
package middleware
