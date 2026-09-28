// Package channel holds the channel use cases: listing, creating and
// finding an organisation's channels.
//
// Feature: channel (feature map in docs/architecture/README.md), which owns
// the channel table. Exported API: Service, the Store interface it needs,
// DefaultName, and ErrNotFound, ErrInvalidName and ErrNameTaken. Every call
// takes the caller's membership from authz; the organisation always comes
// from it, never from the request.
package channel
