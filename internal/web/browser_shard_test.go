package web

import (
	"fmt"
	"hash/fnv"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/go-rod/rod"
)

// Every browser slice runs -run ^TestBrowser$; a rename must break the build.
var _ = TestBrowser

func browserPinnedNames() []string {
	return []string{"read-recovery", "panel-sheet", "width-audit", "progress", "pins", "read-control"}
}

func browserShardNames(names []string, selector string) ([]string, error) {
	pinned := browserPinnedNames()
	for _, name := range pinned {
		if !slices.Contains(names, name) {
			return nil, fmt.Errorf("missing pinned browser scenario %q", name)
		}
	}
	if selector == "" {
		return names, nil
	}
	k, n, _ := strings.Cut(selector, "/")
	index, errK := strconv.Atoi(k)
	total, errN := strconv.Atoi(n)
	if errK != nil || errN != nil || index < 1 || index > total || selector != fmt.Sprintf("%d/%d", index, total) {
		return nil, fmt.Errorf("invalid browser shard %q", selector)
	}
	var selected []string
	for _, name := range names {
		h := fnv.New64a()
		_, _ = h.Write([]byte(name))
		assigned := int(h.Sum64() % uint64(total))
		if i := slices.Index(pinned, name); i >= 0 {
			assigned = i % total
			if i/total%2 == 1 {
				assigned = total - 1 - assigned
			}
		}
		if assigned == index-1 {
			selected = append(selected, name)
		}
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("browser shard %s has no scenarios", selector)
	}
	return selected, nil
}

func TestBrowserShard(t *testing.T) {
	var names []string
	registerBrowserScenarios(func(name string, _ func(*testing.T, *rod.Browser)) { names = append(names, name) })
	totals, err := os.ReadFile("testdata/browser-shard-totals.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range strings.Fields(string(totals)) {
		t.Run(value, func(t *testing.T) {
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 {
				t.Fatalf("invalid shard total %q", value)
			}
			seen, sum := map[string]int{}, 0
			for k := 1; k <= n; k++ {
				selected, err := browserShardNames(names, fmt.Sprintf("%d/%d", k, n))
				if err != nil {
					t.Fatal(err)
				}
				heavy := []string{}
				for _, name := range selected {
					seen[name]++
					if slices.Contains(browserPinnedNames(), name) {
						heavy = append(heavy, name)
					}
				}
				if n == 3 {
					want := []string{browserPinnedNames()[k-1], browserPinnedNames()[6-k]}
					if len(heavy) != 2 || !slices.Contains(heavy, want[0]) || !slices.Contains(heavy, want[1]) {
						t.Fatalf("shard %d heavy = %v, want %v", k, heavy, want)
					}
				}
				sum += len(selected)
				t.Logf("shard %d/%d: %d of %d scenarios", k, n, len(selected), len(names))
			}
			for _, name := range names {
				if seen[name] != 1 {
					t.Errorf("scenario %q assigned %d times", name, seen[name])
				}
			}
			if sum != len(names) {
				t.Errorf("shard sum = %d, want %d", sum, len(names))
			}
		})
	}
}

func TestBrowserShardSelectors(t *testing.T) {
	names := browserPinnedNames()
	for _, selector := range []string{"", "1", "0/3", "4/3", "1/0", "a/b", "1/3x", " 1/3", "01/3", "-1/3", "1/3/5", "1/03", "1/3 ", "+1/3"} {
		t.Run(selector, func(t *testing.T) {
			selected, err := browserShardNames(names, selector)
			if selector == "" {
				if err != nil || !slices.Equal(selected, names) {
					t.Fatalf("unset selector = %v, %v", selected, err)
				}
			} else if err == nil {
				t.Fatal("accepted malformed selector")
			}
		})
	}
	if _, err := browserShardNames(names, "7/7"); err == nil || !strings.Contains(err.Error(), "no scenarios") {
		t.Fatalf("empty shard: %v", err)
	}
	for _, selector := range []string{"", "1/3"} {
		if _, err := browserShardNames(names[1:], selector); err == nil || !strings.Contains(err.Error(), "missing pinned") {
			t.Fatalf("missing pin with selector %q: %v", selector, err)
		}
	}
}
