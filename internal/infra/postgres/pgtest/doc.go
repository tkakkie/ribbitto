// Package pgtest provides isolated PostgreSQL databases and shared fixtures for
// integration tests. OrganizationWithOwner creates an organisation, owner and
// default channel at sequence 1. Organization, Account, Member and Channel allow
// partial setups with explicit sequences, roles, handles and default channels.
// All fixture helpers use the test's context and leave setup rows absent.
package pgtest
