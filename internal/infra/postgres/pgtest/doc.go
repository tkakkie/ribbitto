// Package pgtest provides shared fixtures for integration tests, plus New and
// NewEmpty, which delegate to internal/platform/postgres/pgtest.
// Until step 5, fixtures write org's and identity's rows and a channel with
// its default topic in raw SQL; they import no store or conversation package.
// OrganizationWithOwner creates an organisation, owner and default channel at
// sequence 1. Organization, Account, Member and Channel allow partial setups
// with explicit sequences, roles, handles and default channels. All fixture
// helpers use the test's context and leave setup rows absent.
package pgtest
