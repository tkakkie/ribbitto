// Package middleware supplies shared HTTP controls: security headers, the
// session cookie and the signed-in account, the request body limit, and the
// rate limits on the authentication forms, typing signals and read POSTs. Authentication
// limits count by client key:
// the peer address or, behind a trusted proxy, the rightmost untrusted
// X-Forwarded-For address; an IPv6 client counts as its /64, and its /48 is
// charged too. The independent member limiters accepts trusted organisation/member
// IDs from the authenticated web adapter, each sharing a budget across channels,
// topics, sessions and addresses. Both limiters live in memory per process
// (docs/architecture/rate-limits.md); neither accesses feature state or tables.
package middleware
