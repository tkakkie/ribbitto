// Package middleware supplies shared HTTP controls: security headers, the
// session cookie and the signed-in account, the request body limit, and the
// rate limits on the authentication forms, typing signals, read POSTs and
// channel creation, branching and sidebar resyncs. Authentication limits
// count by client key: the peer address or, behind a trusted proxy, the rightmost untrusted
// X-Forwarded-For
// address; an IPv6 client counts as its /64, and its /48 is charged too.
// Authentication limits warn once per instance when a loopback or private peer
// forwards addresses with no trusted proxies configured, without logging addresses.
// The independent typing, read, creation, branch and resync limiters accept trusted
// organisation/member IDs from the authenticated web adapter. Typing and
// reads each share a budget across channels, topics, sessions and addresses;
// creation's budget is spent only by creating channels, from any session or
// address. Branch requests share a budget across channels, topics, sessions
// and addresses. Resyncs share a budget across all of a member's tabs and
// sessions.
// All limiters live in memory per process
// (docs/architecture/rate-limits.md); none accesses feature state or tables.
package middleware
