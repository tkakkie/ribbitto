package main

import "strings"

// browserRecord retains bounded scope evidence independently of protocol maps.
// Counts include every leaf; names/results stop after the first excess leaf.
type browserRecord struct {
	importPath, run, namedState string
	outside, outsidePackage     bool
	leaves, retained            int
	entries                     [browserLeafLimit + 1]browserLeaf
}

type browserLeaf struct{ name, state string }

// retainBrowserScope reserves fixed storage before reading any untrusted event.
func (a *containerAggregator) retainBrowserScope(importPath, run string) {
	if !a.charge(aggregatorBrowserCharge) {
		return
	}
	a.record.browser = &browserRecord{importPath: a.clone(importPath), run: a.clone(run)}
}

// finishBrowser uses already charged, independently allocated names. Retaining
// them cannot pin package maps; no allocation is allowed in the decision pass.
func (a *containerAggregator) finishBrowser() {
	b := a.record.browser
	if b == nil {
		return
	}
	for pkg, p := range a.packages {
		if pkg != b.importPath {
			b.outsidePackage = true
		}
		for _, t := range p.tests {
			if !t.started {
				continue
			}
			if pkg != b.importPath {
				b.outside, b.outsidePackage = true, true
				continue
			}
			if t.name == b.run {
				b.namedState = t.state
			}
			if t.name != b.run && !browserDescendant(t.name, b.run) && !browserDescendant(b.run, t.name) {
				b.outside = true
			}
			if !t.parent && (t.name == b.run || browserDescendant(t.name, b.run)) {
				b.leaves++
				if b.retained < len(b.entries) {
					b.entries[b.retained] = browserLeaf{t.name, t.state}
					b.retained++
				}
			}
		}
	}
}

// browserDescendant avoids constructing a potentially identity-sized prefix in
// the decision pass, which must allocate nothing even after a cap.
func browserDescendant(name, parent string) bool {
	return len(name) > len(parent) && strings.HasPrefix(name, parent) && name[len(parent)] == '/'
}
