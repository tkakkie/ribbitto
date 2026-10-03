// Package pgxbridge unwraps the platform's opaque Tx and Snapshot to their
// pgx transaction. Only stores import it: depguard allows it under
// **/internal/postgres/** and, until the module migration's last step,
// internal/infra/postgres (decision 26). Use cases never see pgx.
package pgxbridge
