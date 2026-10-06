// Package identitytest holds raw-SQL test fixtures for identity's account
// table (decision 29). Only tests and higher fixture packages import it;
// production code never does. It imports no store or wiring, so a fixture
// can build a state that identity's use cases never would.
package identitytest
