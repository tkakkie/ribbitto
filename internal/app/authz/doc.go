// Package authz forwards to the org module's root (internal/org) during
// migration step 3. Only internal/web and cmd/* still import it; step 3.1b
// (#460) switches them to org and removes this package. Nothing new may
// import it.
package authz
