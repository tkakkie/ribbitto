// Package middleware supplies shared HTTP controls: security headers, the
// session cookie and the signed-in account, the request body limit, and the
// rate limits on the authentication forms. Those limits count by client key:
// the peer address or, behind a trusted proxy, the rightmost untrusted
// X-Forwarded-For address; an IPv6 client counts as its /64, and its /48 is
// charged too (docs/architecture/rate-limits.md).
package middleware
