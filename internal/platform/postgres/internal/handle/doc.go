// Package handle defines the opaque transaction and snapshot handles. It is
// internal to internal/platform/postgres so that only the platform (which
// opens them) and its bridge (which unwraps them for stores) can see the pgx
// transaction inside; module roots and use cases see only the type names.
package handle
