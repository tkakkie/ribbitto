package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// Version 1 is pre-registered in #865; return fresh slices so callers cannot
// change subsequent windows' protocol. Durations here are seconds, limits ms
// unless the gate's unit says otherwise. Percentile triples are p50/p95/p99.
type stage2Protocol struct {
	Version, WindowSeconds, TrendWarmupSeconds, DrainSeconds, Rounds int
	Groups                                                           [4]stage2Group
	Arms                                                             [2]stage2Arm
	ArmOrder                                                         [3][2]string
	Fixture                                                          stage2Fixture
	Stability                                                        stage2Stability
	Gates                                                            []stage2Gate
	Requirements                                                     []string
}
type stage2Group struct{ Streams, PostsPerSecond, ReadsPerSecond, PagesPerSecond, ReplacementsPerSecond int }
type stage2Arm struct {
	Name                string
	Async               bool
	C, W, M, R, E, P, H int
}
type stage2Fixture struct {
	Organizations, Channels, HotChannels, NamedTopics, MessagesPerChannel int
	FeedFraction                                                          float64
	ReconnectDelayMS, ReconnectJitterMS, PostAttempts                     int
}
type stage2Gate struct {
	ID, Scope, Unit, Rule string
	Limits                []float64
}
type stage2Stability struct {
	AdjacentSeconds, SlideSeconds, BoundaryToleranceSeconds, MaxGapSeconds, BucketSeconds, MinPerBucket int
	Cumulative                                                                                          [2][2]int
	SpikeSeconds                                                                                        [2]int
}

func fixedStage2() stage2Protocol {
	p := stage2Protocol{Version: 1, WindowSeconds: 600, TrendWarmupSeconds: 60, DrainSeconds: 30, Rounds: 3,
		Groups:    [4]stage2Group{{1000, 5, 20, 5, 1}, {1000, 20, 20, 5, 1}, {5000, 5, 100, 25, 5}, {5000, 20, 100, 25, 5}},
		Arms:      [2]stage2Arm{{"sync", false, 36, 0, 100, 3, 20, 2, 10}, {"async", true, 36, 26, 100, 3, 20, 2, 10}},
		ArmOrder:  [3][2]string{{"sync", "async"}, {"async", "sync"}, {"sync", "async"}},
		Fixture:   stage2Fixture{1, 4, 1, 2, 500, .5, 250, 250, 1},
		Stability: stage2Stability{120, 60, 2, 2, 10, 8, [2][2]int{{60, 240}, {420, 600}}, [2]int{10, 30}}}
	for _, endpoint := range []string{"feed", "topic"} {
		for _, operation := range []string{"post", "read", "page"} {
			limits := []float64{50, 150, 300}
			if operation == "page" {
				limits = []float64{100, 300, 600}
			}
			id := operation + "." + endpoint
			p.Gates = append(p.Gates, stage2Gate{id, "each async window", "ms", "p50/p95/p99 <= limits", limits},
				stage2Gate{id + ".paired", "each paired percentile", "fraction,ms", "async <= sync + max(fraction*sync, floor)", []float64{.2, 10}})
		}
	}
	p.Gates = append(p.Gates,
		stage2Gate{"normal-503", "establishment/posts/reads/pages", "count", "<= limit", []float64{0}},
		stage2Gate{"replacement-503", "scheduled logical replacements (never attempts)", "fraction,ms", "affected fraction <= limit; every recovery <= limit", []float64{.001, 2000}},
		stage2Gate{"acquired", "async window", "connections", "p95/p99/max <= limits", []float64{32, 35, 36}},
		stage2Gate{"saturation", "async window", "fraction,count", "fraction at C <= limit; consecutive samples at C < limit", []float64{.01, 3}},
		stage2Gate{"empty-acquire", "async window", "fraction", "delta empty / delta acquire <= limit", []float64{.01}},
		stage2Gate{"empty-wait", "async window", "ms", "delta empty wait / delta empty count <= limit; zero when none", []float64{5}},
		stage2Gate{"waiting-pressure", "async whole window and every planned 60s bucket", "callers", "delta empty wait / elapsed <= limit", []float64{.1}},
		stage2Gate{"message-delivery", "continuously connected; HTTP response to complete receipt", "ms", "p95 <= limit", []float64{250}},
		stage2Gate{"post-freshness", "covering witnessed sidebar after post response", "ms", "p95/p99 <= limits", []float64{2000, 5000}},
		stage2Gate{"read-freshness", "covering witnessed sidebar after read response", "ms", "p95/p99 <= limits", []float64{2000, 5000}},
		stage2Gate{"stability", "async; exclude warm-up only from trends", "fraction,bound-fraction,ms", "adjacent 120s means slide 60s; [60,240) vs [420,600); length max(percentage,bound-fraction); age max(percentage,ms)", []float64{.05, .005, 10}},
		stage2Gate{"complete", "both arms", "count", "missing/missed/timeouts/unexpected statuses/resets/disconnects/live errors/unattributed cancellations/closed admission/underload/telemetry gaps must be zero", []float64{0}})
	p.Requirements = []string{
		"real serve, fresh disposable PostgreSQL, authenticated HTTP/1.1 SSE via loopback HTTP target (no HTTPS negotiation); verify startup settings and metrics",
		"N distinct members, one stream each; hot feed interests sidebar,messages,typing; initial snapshots before traffic",
		"independent schedules; posts/reads/pages each 50:50 feed/topic; changed reads only; rotate members; include posting implicit reads",
		"churn retains last complete durable cursor; delay plus seeded uniform jitter; never wait for capacity; no POST retries",
		"warm-up included in latency/errors; separate drain cannot repair underload or telemetry",
		"telemetry each second; boundaries within 2s, increasing nonnegative offsets, gaps <=2s, >=8 samples per planned 10s bucket including warm-up; retain 10s/30s diagnostic spikes",
		"every scheduled operation succeeds; retain failures, dispatch lateness and scheduled-to-response; never success-only percentiles",
		"record every attempt/status, logical affected replacements, retries/outages; reconcile 503 with refused_capacity/refused_closed; unexpected 429/other failures fail",
		"sampled occupancy maxima are not exact peaks; cumulative waits are not percentiles; attribute cancellations to churn/shutdown",
		"same-snapshot committed event and persisted-read-state witness with actual rendered counts; disposable only, off by default, separate review",
		"retain negative/early response-relative receipts and scheduled-to-receipt p50/p95/p99; separate planned-offline replay; complete replay/final snapshots required",
		"functional prerequisites in both modes: multi-tab, presence, authorization/revocation, ordering, replay, typing, heartbeat, cancellation",
		"rounds 1/3 sync then async, round 2 async then sync; identical fixture/traffic seeds within pair; async 3/3 each group; report all results and median/min-max",
		"sync database URL pool_max_conns=36 (async-off ignores budget variables); 36/26 validation only; activation separately approved",
		"quiet fixed machine/resources; exclusive MEASURING marker, pre-load <=CPU/2, interference checks; no concurrent windows/race detector",
		"start only on explicit maintainer instruction after review and functional checks; no initial connection comparison; evidence-triggered 36/26 vs 38/26 only"}
	return p
}

type stage2Config struct {
	Group                    stage2Group
	Arm                      stage2Arm
	Round                    int
	FixtureSeed, TrafficSeed uint64
}
type stage2Effective struct {
	Startup    stage2Arm
	MaxStreams int
	Metrics    json.RawMessage
}
type stage2Sample struct {
	OffsetNS int64
	Phase    string
	Metrics  json.RawMessage
}
type stage2Latency struct {
	Count               uint64
	P50MS, P95MS, P99MS float64
}
type stage2Operation struct {
	Operation, Endpoint                                                     string
	Scheduled, Executed, Succeeded, Changed, Noop, Missed, Timeouts, Failed uint64
	HTTP, ScheduledToResponse, DispatchLateness                             stage2Latency
}
type stage2Attempt struct {
	Operation, Endpoint, Outcome string
	LogicalID, Attempt           uint64
	Status                       int
	OffsetNS, OutageNS           int64
}
type stage2Refusals struct {
	ScheduledReplacements, AffectedReplacements, Recovered, Attempts, Refused503, Retries uint64
	CancelledAcquisitions, AttributedCancellations                                        uint64
	MaxOutageNS                                                                           int64
}

// Early counts strictly negative response-relative receipts; MinMS stays signed.
type stage2ReceiptLatency struct {
	Count, Early               uint64
	MinMS, P50MS, P95MS, P99MS float64
}
type stage2Delivery struct {
	Message, PostFreshness, ReadFreshness                                                 stage2ReceiptLatency
	ScheduledToReceipt, OfflineReplay                                                     stage2Latency
	Expected, Received, Missing, FinalSnapshotsMissing, Resets, LiveErrors, ReplayMissing uint64
}
type stage2Verdict struct {
	ID, State string
	Measured  map[string]float64
}
type stage2Window struct {
	Version    int
	Kind       string
	Protocol   stage2Protocol
	Config     stage2Config
	Effective  stage2Effective
	Samples    []stage2Sample
	Operations []stage2Operation
	Attempts   []stage2Attempt
	Refusals   stage2Refusals
	Delivery   stage2Delivery
	Gates      []stage2Verdict
}
type stage2WindowRef struct {
	State, Reason, File, SHA256 string
	Config                      stage2Config
	Effective                   *stage2Effective
}
type stage2Manifest struct {
	Version  int
	Kind     string
	Protocol stage2Protocol
	Host     struct {
		CPUCount, NOFILESoft, NOFILEHard                  int
		CPU, Go, PostgreSQL, Resources, ProcessConditions string
	}
	Windows []stage2WindowRef
	Gates   []stage2Verdict
}

// Reflection removes duplicated required-field lists for every nested record.
// Raw metrics preserve the complete server snapshot rather than loadgen's old
// partial snapshot. Schema validity does not claim a measurement passed.
func stage2Shape(raw json.RawMessage, typ reflect.Type) error {
	if typ.Kind() == reflect.Pointer {
		if string(raw) == "null" {
			return nil
		}
		return stage2Shape(raw, typ.Elem())
	}
	if string(raw) == "null" {
		return fmt.Errorf("null %s", typ)
	}
	if typ == reflect.TypeOf(json.RawMessage{}) {
		_, err := requiredObject(raw, "database", "pool", "runtime", "streams", "scheduler", "delivery")
		return err
	}
	switch typ.Kind() {
	case reflect.Struct:
		obj, err := requiredObject(raw)
		if err != nil || len(obj) != typ.NumField() {
			return fmt.Errorf("invalid record fields: %v", err)
		}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			value, ok := obj[field.Name]
			if !ok {
				return fmt.Errorf("missing required field %s", field.Name)
			}
			if err := stage2Shape(value, field.Type); err != nil {
				return fmt.Errorf("%s: %w", field.Name, err)
			}
		}
	case reflect.Slice, reflect.Array:
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return err
		}
		if typ.Kind() == reflect.Array && len(items) != typ.Len() {
			return fmt.Errorf("wrong array length")
		}
		for _, item := range items {
			if err := stage2Shape(item, typ.Elem()); err != nil {
				return err
			}
		}
	case reflect.Map:
		obj, err := requiredObject(raw)
		if err != nil {
			return err
		}
		for _, item := range obj {
			if err := stage2Shape(item, typ.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

func decodeStage2(raw []byte, dst any) error {
	if dst == nil || reflect.TypeOf(dst).Kind() != reflect.Pointer || reflect.ValueOf(dst).IsNil() {
		return fmt.Errorf("stage-2 destination must be a nonnil pointer")
	}
	if err := stage2Shape(raw, reflect.TypeOf(dst).Elem()); err != nil {
		return fmt.Errorf("stage-2 fields: %w", err)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("decoding stage-2: %w", err)
	}
	var version int
	var protocol stage2Protocol
	var configs []stage2Config
	var gates []stage2Verdict
	switch v := dst.(type) {
	case *stage2Window:
		for _, receipt := range []stage2ReceiptLatency{v.Delivery.Message, v.Delivery.PostFreshness, v.Delivery.ReadFreshness} {
			if receipt.Early > receipt.Count || (receipt.MinMS < 0) != (receipt.Early > 0) || receipt.MinMS > receipt.P50MS || (receipt.Count == 0 && receipt.MinMS != 0) {
				return fmt.Errorf("invalid signed receipt evidence")
			}
		}
		gates = v.Gates
		version, protocol, configs = v.Version, v.Protocol, []stage2Config{v.Config}
		if v.Kind != "window" {
			return fmt.Errorf("expected window")
		}
	case *stage2Manifest:
		gates = v.Gates
		version, protocol = v.Version, v.Protocol
		if v.Kind != "manifest" {
			return fmt.Errorf("expected manifest")
		}
		if len(v.Windows) != len(v.Protocol.Groups)*len(v.Protocol.Arms)*v.Protocol.Rounds {
			return fmt.Errorf("manifest must list all 24 planned windows")
		}
		seen := make(map[stage2Config]bool)
		for _, window := range v.Windows {
			key := window.Config
			key.FixtureSeed, key.TrafficSeed = 0, 0 // Seeds do not identify a planned slot.
			if seen[key] {
				return fmt.Errorf("duplicate planned window")
			}
			seen[key] = true
			hash, err := hex.DecodeString(window.SHA256)
			artifact := strings.TrimSpace(window.File) != "" && err == nil && len(hash) == 32
			switch window.State {
			case "completed":
				if !artifact || window.Effective == nil || window.Reason != "" {
					return fmt.Errorf("completed window needs artifact/effective evidence and no reason")
				}
			case "failed":
				if strings.TrimSpace(window.Reason) == "" || ((window.File != "" || window.SHA256 != "") && !artifact) {
					return fmt.Errorf("failed window needs reason and valid artifact when present")
				}
			case "unrun":
				if strings.TrimSpace(window.Reason) == "" || window.File != "" || window.SHA256 != "" || window.Effective != nil {
					return fmt.Errorf("unrun window needs reason and no artifact/effective evidence")
				}
			default:
				return fmt.Errorf("invalid window state")
			}
			configs = append(configs, window.Config)
		}
	default:
		return fmt.Errorf("unsupported stage-2 destination")
	}
	if version != 1 || !reflect.DeepEqual(protocol, fixedStage2()) {
		return fmt.Errorf("unknown version or changed protocol")
	}
	seenGates := make(map[string]bool, len(protocol.Gates))
	for _, gate := range protocol.Gates {
		seenGates[gate.ID] = false
	}
	for _, gate := range gates {
		seen, known := seenGates[gate.ID]
		if !known {
			return fmt.Errorf("unknown gate ID %q", gate.ID)
		}
		if seen {
			return fmt.Errorf("duplicate gate ID %q", gate.ID)
		}
		if !slices.Contains([]string{"pass", "reject", "inconclusive"}, gate.State) {
			return fmt.Errorf("invalid gate verdict")
		}
		seenGates[gate.ID] = true
	}
	for _, gate := range protocol.Gates {
		if !seenGates[gate.ID] {
			return fmt.Errorf("missing gate ID %q", gate.ID)
		}
	}
	for _, c := range configs {
		if !slices.Contains(protocol.Groups[:], c.Group) || !slices.Contains(protocol.Arms[:], c.Arm) || c.Round < 1 || c.Round > protocol.Rounds {
			return fmt.Errorf("invalid window configuration")
		}
	}
	return nil
}
