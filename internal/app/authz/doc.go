// Package authz is the single place that decides who may see an
// organisation's data. Handlers and, later, the real-time hub call it; they
// never decide access themselves.
package authz
