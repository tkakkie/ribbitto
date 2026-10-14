package web

import "testing"

// Deliberate failure for #992: shows the ci gate failing, not skipping, when
// one test job fails. Reverted in the next commit.
func TestCIGateDemo(t *testing.T) { t.Fatal("deliberate failure for #992") }
