package main

// keep retains head and tail halves without a growing concatenation. The
// command head and stderr keep a prefix instead, matching their retrieval API.
func (a *containerAggregator) keep(o *retainedOutput, text string, limit int, shared bool) {
	if a.record.stop != "" {
		return
	}
	o.total += len(text)
	headOnly := o == &a.record.stdout || o == &a.record.stderr
	headLimit := limit / 2
	if headOnly {
		headLimit = limit
	}
	headCapacity := headLimit
	if shared {
		headCapacity = min(headCapacity, cap(o.head)+aggregatorOutputLimit-a.outputBytes)
	}
	add := min(len(text), headCapacity-len(o.head))
	oldCapacity := cap(o.head)
	if !a.grow(&o.head, len(o.head)+add, headCapacity) {
		return
	}
	o.head = append(o.head, text[:add]...)
	text = text[add:]
	if shared {
		a.outputBytes += cap(o.head) - oldCapacity
	}
	if headOnly || len(text) == 0 {
		return
	}
	tailLimit := limit - headLimit
	if shared {
		tailLimit = min(tailLimit, cap(o.tail)+aggregatorOutputLimit-a.outputBytes)
		if tailLimit < limit-headLimit && len(o.tail)+len(text) > tailLimit {
			o.notRetained = true
		}
	}
	if tailLimit == 0 {
		return
	}
	add = min(len(text), tailLimit)
	old := len(o.tail)
	oldCapacity = cap(o.tail)
	need := min(tailLimit, old+add)
	if !a.grow(&o.tail, need, tailLimit) {
		return
	}
	if add == tailLimit {
		o.tail = o.tail[:add]
		copy(o.tail, text[len(text)-add:])
	} else {
		keep := min(old, tailLimit-add)
		copy(o.tail, o.tail[old-keep:])
		o.tail = o.tail[:keep]
		o.tail = append(o.tail, text[len(text)-add:]...)
	}
	if shared {
		a.outputBytes += cap(o.tail) - oldCapacity
	}
}

// drop releases capacity as soon as a pass makes its diagnostics unnecessary.
func (a *containerAggregator) drop(o *retainedOutput) {
	a.record.accounted -= cap(o.head) + cap(o.tail)
	*o = retainedOutput{}
}

// budget transfers a finished test's retained bytes into the shared pool.
// The skip-confirmation slot is independent and survives even a zero allowance.
func (a *containerAggregator) budget(o *retainedOutput) {
	available := aggregatorOutputLimit - a.outputBytes
	kept := cap(o.head) + cap(o.tail)
	if kept <= available {
		a.outputBytes += kept
		return
	}
	old := *o
	*o = retainedOutput{}
	a.keep(o, string(old.head), available, true)
	a.keep(o, string(old.tail), available, true)
	o.total, o.notRetained = old.total, true
	a.record.accounted -= cap(old.head) + cap(old.tail)
}

// retainCommandHead copies only the command prefix into the same record type.
// Test2json writers never call this: a pass must leave no raw output behind.
func (a *containerAggregator) retainCommandHead(data []byte) {
	a.record.mu.Lock()
	defer a.record.mu.Unlock()
	if a.record.stop != "" || a.record.sealed {
		return
	}
	a.keep(&a.record.stdout, string(data[:min(len(data), aggregatorStdoutLimit)]), aggregatorStdoutLimit, false)
	a.record.stdout.total += max(0, len(data)-aggregatorStdoutLimit)
}
