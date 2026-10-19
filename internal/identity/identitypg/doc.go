// Package identitypg wires identity's use cases to its PostgreSQL store
// (decision 26). Only cmd/*, compose and tests import it; it holds
// no business logic. AccountCreatorIn binds identity's creator to a caller's
// transaction; compose adapts it to org's factory.
package identitypg
