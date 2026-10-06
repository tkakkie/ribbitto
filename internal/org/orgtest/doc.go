// Package orgtest holds raw-SQL test fixtures for org's organization and
// member tables (decision 29). Only tests and conversationtest import it;
// production code never does. Its fixtures may build states setup and
// sign-up never create, such as an event_seq without its event row or an
// organisation without a setup row, so tests can choose each value.
package orgtest
