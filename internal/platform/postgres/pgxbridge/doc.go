// Package pgxbridge unwraps the platform's opaque Tx and Snapshot to their
// pgx transaction. Only stores under **/internal/postgres/** import it
// (decision 26). Use cases never see pgx.
package pgxbridge
