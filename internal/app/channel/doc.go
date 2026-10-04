// Package channel holds the channel use cases: listing, creating and
// finding an organisation's channels.
//
// Feature: channel (feature map in docs/architecture/features.md), which owns
// the channel table. Exported API: Service and the Store interface it needs.
// Channel, its name rule, errors and default name belong to conversation. Every call
// takes the caller's membership from org.Authorizer; the organisation always comes
// from it, never from the request.
package channel
