package main

import "sync"

// containerRecord is built once and shared by reference for the run and log
// readers. It must never be copied: its mutex also makes go vet enforce this.
// Readers may inspect it only after finish has sealed both writers.
type containerRecord struct {
	mu                                                sync.Mutex
	complete, sealed, stderrNotServable               bool
	stop, capName                                     string
	events, afterEvent, streamed, accounted           int
	stdoutBytes                                       int
	packages, leaves                                  summaryCounts
	confirmedFailures, failedLeaves, namesNotRetained int
	entries                                           []*aggregatorEntry
	indexStorage                                      [aggregatorIndexLimit]*aggregatorEntry
	skipStorage                                       [aggregatorIndexLimit]string
	disallowedSkips                                   int
	allowed, skipped                                  []string
	stderr, stdout, compiler, packageOutput           retainedOutput
	panicLine, timeoutLine                            string
}

// retainedOutput separates diagnostic omission from absence of evidence.
type retainedOutput struct {
	head, tail  []byte
	total       int
	notRetained bool
}

// aggregatorEntry keeps raw identities; renderers must quote untrusted text.
// Result links are embedded in protocol state so the index can be selected in
// arrival order after later descendants have established which tests are leaves.
type aggregatorEntry struct {
	kind, pkg, name, state string
	skipName               string
	output                 retainedOutput
	next                   *aggregatorEntry
	parent, started        bool
	allowed                bool
	slot                   []byte
	message                bool
	match                  string
}

// aggregatorPackage owns placeholders for output-before-run and slash ancestors,
// both accepted by the oracle. Only explicit run events consume the test cap.
type aggregatorPackage struct {
	aggregatorEntry
	tests map[string]*aggregatorEntry
	runs  int
}
