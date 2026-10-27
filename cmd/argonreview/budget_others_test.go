package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// otherFixture uses real inspect field shapes, including the leading slash.
func otherFixture(kind, slot, status string) otherInspect {
	var c otherInspect
	c.ID, c.Name = strings.Repeat("a", 64), "/foreign-private-name"
	c.Config.Image = "private-image"
	c.Config.Labels = map[string]string{otherSlotLabel: slot, "private-label": "private-value"}
	c.State.Status = status
	c.HostConfig.Memory, c.HostConfig.MemorySwap = otherUnit, otherUnit
	if kind == "verification" {
		c.Name = "/argonreview-check-" + strings.Repeat("b", 32)
		c.Config.Image = containerVerifyImage
		c.Config.Labels[containerOwnerLabel] = containerOwnerValue
		if len(slot) == 3 {
			c.Config.Labels[otherClassLabel] = "browser"
			c.HostConfig.Memory, c.HostConfig.MemorySwap = 2*otherUnit, 2*otherUnit
		}
	} else if kind != "unregistered" {
		c.Config.Labels[otherBudgetLabel] = kind
	}
	return c
}

// otherEdit avoids shared maps between table rows and returns a modified copy.
func otherEdit(c otherInspect, edit func(*otherInspect)) otherInspect {
	labels := make(map[string]string, len(c.Config.Labels))
	for k, v := range c.Config.Labels {
		labels[k] = v
	}
	c.Config.Labels = labels
	edit(&c)
	return c
}

// otherMemory varies the memory rule alone, keeping swap consistent with it.
func otherMemory(c otherInspect, memory int64) otherInspect {
	return otherEdit(c, func(c *otherInspect) { c.HostConfig.Memory, c.HostConfig.MemorySwap = memory, memory })
}

// otherLabel changes a single registration field without changing its peers.
func otherLabel(c otherInspect, key, value string) otherInspect {
	return otherEdit(c, func(c *otherInspect) { c.Config.Labels[key] = value })
}

// otherAssert pins the complete result and the report's privacy boundary.
func otherAssert(t *testing.T, got, want otherContainers, input []otherInspect) {
	t.Helper()
	if got != want {
		t.Fatalf("result = %+v, want %+v", got, want)
	}
	for _, c := range input {
		for _, secret := range []string{c.ID, c.Name, strings.TrimPrefix(c.Name, "/"), c.Config.Image, c.Config.Labels["private-label"]} {
			if secret != "" && strings.Contains(got.line, secret) {
				t.Fatal("report disclosed container data")
			}
		}
		for key, value := range c.Config.Labels {
			// Fixed protocol words can also occur in fixed report text. Arbitrary
			// label data must never cross this boundary, including unknown classes.
			if value != "" && value != "exclusive" && value != "dev" && value != "browser" && value != containerOwnerValue && strings.Contains(got.line, value) {
				t.Fatalf("report disclosed value of label %q", key)
			}
		}
	}
}

// TestBudgetVerificationConsistency isolates each reservation inconsistency.
func TestBudgetVerificationConsistency(t *testing.T) {
	one := otherFixture("verification", "0", "running")
	two := otherFixture("verification", "0,2", "running")
	rows := []struct {
		name string
		c    otherInspect
		ok   bool
	}{
		{"one slot", one, true}, {"two browser slots", two, true},
		{"memory zero", otherMemory(one, 0), false},
		{"memory one GiB", otherMemory(one, otherUnit/2), false},
		{"memory three GiB", otherMemory(one, 3*otherUnit/2), false},
		{"two slots memory two GiB", otherMemory(two, otherUnit), false},
		{"two slots memory six GiB", otherMemory(two, 3*otherUnit), false},
		{"swap unlimited", otherEdit(one, func(c *otherInspect) { c.HostConfig.MemorySwap = -1 }), false},
		{"swap larger", otherEdit(one, func(c *otherInspect) { c.HostConfig.MemorySwap++ }), false},
		{"swap smaller", otherEdit(one, func(c *otherInspect) { c.HostConfig.MemorySwap-- }), false},
		{"two slots without class", otherEdit(two, func(c *otherInspect) { delete(c.Config.Labels, otherClassLabel) }), false},
		{"class with one slot", otherLabel(one, otherClassLabel, "browser"), false},
		{"three slots", otherLabel(two, otherSlotLabel, "0,1,2"), false},
		{"another image", otherEdit(one, func(c *otherInspect) { c.Config.Image = "another-private-image" }), false},
		{"wrong owner", otherLabel(one, containerOwnerLabel, "private-owner"), false},
		{"owner with foreign name", otherEdit(one, func(c *otherInspect) { c.Name = "/private-foreign" }), false},
		{"foreign name with arbitrary owner value", otherEdit(one, func(c *otherInspect) {
			c.Name = "/private-foreign"
			c.Config.Labels[containerOwnerLabel] = "private-owner"
		}), false},
		{"foreign name with empty owner value", otherEdit(one, func(c *otherInspect) { c.Name = "/private-foreign"; c.Config.Labels[containerOwnerLabel] = "" }), false},
		{"missing owner", otherEdit(one, func(c *otherInspect) { delete(c.Config.Labels, containerOwnerLabel) }), false},
		{"empty owner", otherLabel(one, containerOwnerLabel, ""), false},
		{"missing slot", otherEdit(one, func(c *otherInspect) { delete(c.Config.Labels, otherSlotLabel) }), false},
		{"malformed slot", otherLabel(one, otherSlotLabel, "0,"), false},
		{"out of range slot", otherLabel(one, otherSlotLabel, "3"), false},
		{"unknown class", otherLabel(one, otherClassLabel, "private-class"), false},
		{"exited still reserved", otherEdit(one, func(c *otherInspect) { c.State.Status = "exited" }), true},
		{"dead still reserved", otherEdit(one, func(c *otherInspect) { c.State.Status = "dead" }), true},
		{"removing still reserved", otherEdit(one, func(c *otherInspect) { c.State.Status = "removing" }), true},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			want := otherContainers{verification: 1, total: 1, line: otherVerificationLine}
			if row.ok {
				want.line, want.held[0] = "", true
				want.held[2] = row.c.Config.Labels[otherSlotLabel] == "0,2"
			}
			otherAssert(t, otherClassify([]otherInspect{row.c}, nil), want, []otherInspect{row.c})
		})
	}
}

// TestBudgetOthers pins counts, held slots, privacy, and order-independent precedence.
func TestBudgetOthers(t *testing.T) {
	dev := otherFixture("dev", "1", "running")
	verify := otherFixture("verification", "1", "running")
	exclusive := otherFixture("exclusive", "0,2", "running")
	foreign := otherFixture("unregistered", "2", "running")
	// Collisions must be between distinct containers, not repeated list rows.
	verify.ID, exclusive.ID, foreign.ID = strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 64)
	dev2 := otherEdit(dev, func(c *otherInspect) { c.ID = strings.Repeat("e", 64); c.Name = "/another-private-name" })
	bad := otherLabel(dev, otherSlotLabel, "1,1")
	rows := []struct {
		name string
		list []otherInspect
		err  error
		want otherContainers
	}{
		{"empty", nil, nil, otherContainers{}},
		{"dev running", []otherInspect{dev}, nil, otherContainers{dev: 1, total: 1, held: [3]bool{false, true}}},
		{"dev created", []otherInspect{otherEdit(dev, func(c *otherInspect) { c.State.Status = "created" })}, nil, otherContainers{dev: 1, total: 1, held: [3]bool{false, true}}},
		{"dev paused", []otherInspect{otherEdit(dev, func(c *otherInspect) { c.State.Status = "paused" })}, nil, otherContainers{dev: 1, total: 1, held: [3]bool{false, true}}},
		{"dev restarting", []otherInspect{otherEdit(dev, func(c *otherInspect) { c.State.Status = "restarting" })}, nil, otherContainers{dev: 1, total: 1, held: [3]bool{false, true}}},
		{"dev two slots small memory", []otherInspect{otherLabel(otherMemory(dev, 1), otherSlotLabel, "2,0")}, nil, otherContainers{dev: 1, total: 1, held: [3]bool{true, false, true}}},
		{"dev no limit", []otherInspect{otherMemory(dev, 0)}, nil, otherContainers{line: otherUnlimitedLine, dev: 1, total: 1}},
		{"dev negative memory", []otherInspect{otherMemory(dev, -1)}, nil, otherContainers{line: otherRegistrationLine, dev: 1, total: 1}},
		{"dev swap", []otherInspect{otherEdit(dev, func(c *otherInspect) { c.HostConfig.MemorySwap = -1 })}, nil, otherContainers{line: otherRegistrationLine, dev: 1, total: 1}},
		{"dev too large", []otherInspect{otherMemory(dev, otherUnit+1)}, nil, otherContainers{line: otherRegistrationLine, dev: 1, total: 1}},
		{"dev missing slot", []otherInspect{otherEdit(dev, func(c *otherInspect) { delete(c.Config.Labels, otherSlotLabel) })}, nil, otherContainers{line: otherRegistrationLine, dev: 1, total: 1}},
		{"dev malformed slot", []otherInspect{bad}, nil, otherContainers{line: otherRegistrationLine, dev: 1, total: 1}},
		{"dev out of range", []otherInspect{otherLabel(dev, otherSlotLabel, "3")}, nil, otherContainers{line: otherRegistrationLine, dev: 1, total: 1}},
		{"exclusive running", []otherInspect{exclusive}, nil, otherContainers{line: otherExclusiveLine, exclusive: 1, total: 1}},
		{"exclusive created ignores limits", []otherInspect{otherMemory(otherEdit(exclusive, func(c *otherInspect) { c.State.Status = "created" }), 0)}, nil, otherContainers{line: otherExclusiveLine, exclusive: 1, total: 1}},
		{"exclusive ignores malformed slot", []otherInspect{otherLabel(exclusive, otherSlotLabel, "private-slot")}, nil, otherContainers{line: otherExclusiveLine, exclusive: 1, total: 1}},
		{"exclusive exited", []otherInspect{otherEdit(exclusive, func(c *otherInspect) { c.State.Status = "exited" })}, nil, otherContainers{}},
		{"unregistered limited", []otherInspect{foreign}, nil, otherContainers{line: otherUnregisteredLine, other: 1, total: 1}},
		{"unregistered exited", []otherInspect{otherEdit(foreign, func(c *otherInspect) { c.State.Status = "exited" })}, nil, otherContainers{}},
		{"dev dead", []otherInspect{otherEdit(dev, func(c *otherInspect) { c.State.Status = "dead" })}, nil, otherContainers{}},
		{"dev removing", []otherInspect{otherEdit(dev, func(c *otherInspect) { c.State.Status = "removing" })}, nil, otherContainers{}},
		{"other budget value", []otherInspect{otherLabel(foreign, otherBudgetLabel, "private-budget")}, nil, otherContainers{line: otherUnregisteredLine, other: 1, total: 1}},
		{"duplicate dev verification", []otherInspect{dev, verify}, nil, otherContainers{line: otherDuplicateLine, verification: 1, dev: 1, total: 2, held: [3]bool{false, true}}},
		{"duplicate dev dev", []otherInspect{dev, dev2}, nil, otherContainers{line: otherDuplicateLine, dev: 2, total: 2, held: [3]bool{false, true}}},
		{"unknown state", []otherInspect{otherEdit(dev, func(c *otherInspect) { c.State.Status = "private-state" })}, nil, otherContainers{line: otherStateLine}},
		{"missing state", []otherInspect{otherEdit(dev, func(c *otherInspect) { c.State.Status = "" })}, nil, otherContainers{line: otherStateLine}},
		{"unknown verification state", []otherInspect{otherEdit(verify, func(c *otherInspect) { c.State.Status = "" })}, nil, otherContainers{line: otherStateLine, verification: 1, total: 1, held: [3]bool{false, true}}},
		{"invalid inspected ID", []otherInspect{otherEdit(dev, func(c *otherInspect) { c.ID = "private-id" })}, nil, otherContainers{line: otherStateLine, dev: 1, total: 1, held: [3]bool{false, true}}},
		{"state error first", []otherInspect{bad, exclusive}, errors.New("private-error"), otherContainers{line: otherStateLine, dev: 1, exclusive: 1, total: 2}},
		{"verification before dev", []otherInspect{bad, otherMemory(verify, 0), exclusive}, nil, otherContainers{line: otherVerificationLine, verification: 1, dev: 1, exclusive: 1, total: 3}},
		{"registration before no limit", []otherInspect{bad, otherMemory(dev, 0), exclusive}, nil, otherContainers{line: otherRegistrationLine, dev: 2, exclusive: 1, total: 3}},
		{"no limit before unregistered", []otherInspect{otherMemory(dev, 0), foreign, exclusive}, nil, otherContainers{line: otherUnlimitedLine, dev: 1, other: 1, exclusive: 1, total: 3}},
		{"unregistered before duplicate", []otherInspect{dev, dev2, foreign, exclusive}, nil, otherContainers{line: otherUnregisteredLine, dev: 2, other: 1, exclusive: 1, total: 4, held: [3]bool{false, true}}},
		{"duplicate before exclusive", []otherInspect{dev, dev2, exclusive}, nil, otherContainers{line: otherDuplicateLine, dev: 2, exclusive: 1, total: 3, held: [3]bool{false, true}}},
		{"all four kinds", []otherInspect{dev, otherLabel(verify, otherSlotLabel, "0"), exclusive, foreign}, nil, otherContainers{line: otherUnregisteredLine, verification: 1, dev: 1, exclusive: 1, other: 1, total: 4, held: [3]bool{true, true}}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			otherAssert(t, otherClassify(row.list, row.err), row.want, row.list)
			reversed := slices.Clone(row.list)
			slices.Reverse(reversed)
			otherAssert(t, otherClassify(reversed, row.err), row.want, reversed)
		})
	}
}

// TestBudgetOthersSlotLabel refuses ambiguous or noncanonical reservations.
func TestBudgetOthersSlotLabel(t *testing.T) {
	rows := []struct {
		label string
		want  []int
	}{
		{"0", []int{0}}, {"2", []int{2}}, {"0,2", []int{0, 2}}, {"2,0", []int{2, 0}},
		{"", nil}, {"3", nil}, {"-1", nil}, {"1,1", nil}, {"0,1,2", nil}, {"01", nil},
		{" 1", nil}, {"1 ", nil}, {"1,", nil}, {",1", nil}, {"a", nil}, {"1;2", nil}, {strings.Repeat("1", 100), nil},
	}
	for _, row := range rows {
		t.Run(row.label, func(t *testing.T) {
			got, ok := otherSlotLabelSlots(row.label)
			if ok != (row.want != nil) || ok && !slices.Equal(got, row.want) {
				t.Fatalf("slots = %v, %v, want %v", got, ok, row.want)
			}
		})
	}
}

// otherFake records every transport call and supports disappearance races.
type otherFake struct {
	inspects   map[string][]byte
	fail       map[string]bool
	listings   []string
	listErr    bool
	listFailAt int
	calls      [][]string
	lists      int
}

// otherFakeDocker assigns distinct full IDs so multi-container fixtures are real.
func otherFakeDocker(t *testing.T, list ...otherInspect) *otherFake {
	t.Helper()
	f := &otherFake{inspects: map[string][]byte{}, fail: map[string]bool{}}
	var ids []string
	for i, c := range list {
		c.ID = fmt.Sprintf("%064x", i+1)
		data, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		f.inspects[c.ID], ids = data, append(ids, c.ID)
	}
	f.listings = []string{strings.Join(ids, "\n")}
	return f
}

// otherQuery rejects unexpected argument shapes as well as recording verbs.
func (f *otherFake) otherQuery(args ...string) ([]byte, error) {
	f.calls = append(f.calls, slices.Clone(args))
	if slices.Equal(args, []string{"ps", "-a", "-q", "--no-trunc"}) {
		if f.listErr || f.listFailAt == f.lists+1 {
			return nil, errors.New("private-list-error")
		}
		listing := f.listings[min(f.lists, len(f.listings)-1)]
		f.lists++
		return []byte(listing), nil
	}
	if len(args) == 4 && slices.Equal(args[:3], []string{"inspect", "--format", "{{json .}}"}) {
		if f.fail[args[3]] {
			return nil, errors.New("private-inspect-error")
		}
		return f.inspects[args[3]], nil
	}
	return nil, errors.New("unexpected query")
}

// TestBudgetOthersPoll covers discovery failures and concurrent disappearance.
func TestBudgetOthersPoll(t *testing.T) {
	dev, exclusive := otherFixture("dev", "1", "running"), otherFixture("exclusive", "", "created")
	id := fmt.Sprintf("%064x", 1)
	rows := []struct {
		name  string
		edit  func(*otherFake)
		want  otherContainers
		calls int
	}{
		{"lists and inspects all", func(*otherFake) {}, otherContainers{line: otherExclusiveLine, dev: 1, exclusive: 1, total: 2, held: [3]bool{false, true}}, 3},
		{"listing error", func(f *otherFake) { f.listErr = true }, otherContainers{line: otherStateLine}, 1},
		{"inspect error still listed", func(f *otherFake) { f.fail[id] = true }, otherContainers{line: otherStateLine, exclusive: 1, total: 1}, 4},
		{"invalid ID", func(f *otherFake) { f.listings[0] += "\nprivate-invalid-id" }, otherContainers{line: otherStateLine}, 1},
		{"uppercase ID", func(f *otherFake) { f.listings[0] = strings.Repeat("A", 64) }, otherContainers{line: otherStateLine}, 1},
		{"invalid JSON", func(f *otherFake) { f.inspects[id] = []byte("{private-invalid-json") }, otherContainers{line: otherStateLine, exclusive: 1, total: 1}, 3},
		{"ID mismatch", func(f *otherFake) { f.inspects[id] = f.inspects[fmt.Sprintf("%064x", 2)] }, otherContainers{line: otherStateLine, exclusive: 1, total: 1}, 3},
		{"vanished", func(f *otherFake) { f.fail[id] = true; f.listings = append(f.listings, fmt.Sprintf("%064x", 2)) }, otherContainers{line: otherExclusiveLine, exclusive: 1, total: 1}, 4},
		{"absence listing error", func(f *otherFake) { f.fail[id] = true; f.listFailAt = 2 }, otherContainers{line: otherStateLine, exclusive: 1, total: 1}, 4},
		{"absence listing invalid", func(f *otherFake) { f.fail[id] = true; f.listings = append(f.listings, "private-invalid-id") }, otherContainers{line: otherStateLine, exclusive: 1, total: 1}, 4},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			f := otherFakeDocker(t, dev, exclusive)
			row.edit(f)
			otherAssert(t, otherPoll(f.otherQuery), row.want, []otherInspect{dev, exclusive})
			if len(f.calls) != row.calls || !slices.Equal(f.calls[0], []string{"ps", "-a", "-q", "--no-trunc"}) {
				t.Fatalf("unexpected calls: %v", f.calls)
			}
			if row.name == "lists and inspects all" && (!slices.Equal(f.calls[1], []string{"inspect", "--format", "{{json .}}", id}) || !slices.Equal(f.calls[2], []string{"inspect", "--format", "{{json .}}", fmt.Sprintf("%064x", 2)})) {
				t.Fatalf("did not inspect every full ID: %v", f.calls)
			}
		})
	}
}
