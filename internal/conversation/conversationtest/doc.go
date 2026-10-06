// Package conversationtest holds raw-SQL test fixtures for conversation's
// channel and topic tables, and the composite organisation with its owner
// and default channel (decision 29). Only tests import it; production code
// never does. The composite writes org's and identity's rows only through
// orgtest and identitytest, and adds no event or setup row.
package conversationtest
