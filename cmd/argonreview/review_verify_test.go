package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Separate failures ensure removing either verification call fails a test.
func TestReviewVerifiesHome(t *testing.T) {
	for _, tc := range []struct {
		name              string
		fail, calls, code int
		probe             bool
	}{
		{"before launch", 1, 1, 1, false},
		{"after run", 2, 2, 1, false},
		{"probe after run", 2, 2, 1, true},
		{"counts", 0, 2, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, fake := newReview(t)
			r.probe = tc.probe
			// Process discovery has its own tests and needs host ps permission.
			r.sup.ops.scan = func(int) ([]int, error) { return nil, nil }
			calls := 0
			refusal := errors.New("review HOME changed")
			r.verifyHome = func() error {
				calls++
				if calls == tc.fail {
					return refusal
				}
				return nil
			}
			var out bytes.Buffer
			code, err := r.run(&out)
			if calls != tc.calls || code != tc.code || (tc.fail != 0 && !errors.Is(err, refusal)) || (tc.fail == 0 && err != nil) {
				t.Fatalf("calls=%d code=%d err=%v", calls, code, err)
			}
			_, seenErr := os.Stat(filepath.Join(fake, "seen"))
			if (tc.fail == 1 && !os.IsNotExist(seenErr)) || (tc.fail != 1 && seenErr != nil) {
				t.Fatalf("agy seen: %v", seenErr)
			}
			if tc.probe {
				if !strings.Contains(out.String(), "Verdict: does not count: "+refusal.Error()) {
					t.Fatalf("verdict: %q", out.String())
				}
			} else if tc.fail != 0 {
				if out.Len() != 0 {
					t.Fatalf("failed run wrote report: %q", out.String())
				}
			} else if want := "Argon review of tkakkie/ribbitto#7 at " + r.pr.Head + "\nLauncher built from " + r.revision + "\n\nNo findings.\n"; out.String() != want {
				t.Fatalf("report: %q; want %q", out.String(), want)
			}
			nothingLeft(t, r.source)
		})
	}
}
