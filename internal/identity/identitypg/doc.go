// Package identitypg wires identity's use cases to its PostgreSQL store
// (decision 26). Only composition roots (cmd/*) and tests import it; it holds
// no business logic. AccountCreatorIn binds identity's creator to a caller's
// transaction; composition roots adapt it to org's factory with a closure.
package identitypg
