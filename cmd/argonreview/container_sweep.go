package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"time"
)

const sweepGrace = 90 * time.Second

var (
	sweepContainerName = regexp.MustCompile(containerNamePattern)
	sweepCopyName      = regexp.MustCompile(`^argonreview-container-[0-9]{1,20}$`)
	sweepID            = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type containerSweep struct {
	copyRoot       *os.File
	resolvedParent string
	runner         dockerRunner
	processes      processTable
	parent         string
	uid            func() int
	now            func() time.Time
	sleep          func(time.Duration)
	sameDevice     func(uint64, uint64) bool
	out            io.Writer
	dry            bool
	err            error
}

func newContainerSweep(out io.Writer, dry bool) *containerSweep {
	return &containerSweep{runner: dockerRunner{ownGroup: true}, processes: psTable{}, parent: containerCopyParent,
		uid: os.Getuid, now: time.Now, sleep: time.Sleep, sameDevice: func(a, b uint64) bool { return a == b }, out: out, dry: dry}
}

func sweepContainers(out io.Writer) error { return newContainerSweep(out, false).run() }

func containerSweepCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) > 1 || len(args) == 1 && args[0] != "--dry-run" {
		_, _ = fmt.Fprintln(stderr, "usage: argonreview container-sweep [--dry-run]")
		return 2
	}
	info, ok := debug.ReadBuildInfo()
	_, err := trusted(info, ok, func() (string, error) { return sourceRevision(os.Getenv("RIBBITTO_ARGON_SOURCE"), git) })
	if err == nil {
		err = newContainerSweep(stdout, len(args) == 1).run()
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "argonreview: %v\n", err)
		return 1
	}
	return 0
}

// Only step names cross the reporting boundary, never command output or file data.
func (s *containerSweep) fail(step string) {
	s.err = errors.Join(s.err, fmt.Errorf("%s: %w", step, errors.New("sweep failed")))
}
func (s *containerSweep) line(format string, args ...any) {
	if _, err := fmt.Fprintf(s.out, format+"\n", args...); err != nil {
		s.fail("writing sweep report")
	}
}
func (s *containerSweep) unreclaimed(kind, name string) {
	s.line("unreclaimed %s %s", kind, name)
	s.fail("reclaiming " + kind)
}

func (s *containerSweep) run() error {
	parent, err := filepath.EvalSymlinks(s.parent)
	if err != nil {
		s.fail("resolving copy parent")
	} else {
		fd, err := openSweepParent(parent)
		if err != nil {
			s.fail("opening copy parent")
		} else {
			s.resolvedParent = parent
			s.copyRoot = os.NewFile(uintptr(fd), parent)
			defer func() { _ = s.copyRoot.Close() }()
		}
	}
	s.clients()
	// Client discovery is repeated for liveness after orphan reclamation.
	s.containers()
	s.copies()
	return s.err
}

func (s *containerSweep) docker(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), containerCleanupTimeout)
	defer cancel()
	output := containerOutput{cancel: cancel}
	code, err := s.runner.run(ctx, args, dockerEnvironment(), output.stdoutWriter(), io.Discard)
	if err != nil || code != 0 || output.over || ctx.Err() != nil {
		return nil, errors.New("docker command failed")
	}
	return bytes.TrimSpace(output.stdout.Bytes()), nil
}

func (s *containerSweep) ids(filtered bool) ([]string, error) {
	args := []string{"ps", "-a", "-q", "--no-trunc"}
	if filtered {
		args = []string{"ps", "-a", "--no-trunc", "--filter", "name=argonreview-check-", "--filter", "label=" + containerOwnerLabel + "=" + containerOwnerValue, "--format", "{{.ID}}"}
	}
	data, err := s.docker(args...)
	if err != nil {
		return nil, fmt.Errorf("listing containers: %w", err)
	}
	ids := strings.Fields(string(data))
	for _, id := range ids {
		if !sweepID.MatchString(id) {
			return nil, errors.New("invalid container listing")
		}
	}
	return ids, nil
}

type sweepMount struct{ Type, Source, Destination string }
type sweepContainer struct {
	ID      string `json:"Id"`
	Name    string
	Created time.Time
	Config  struct {
		Image  string
		Labels map[string]string
	}
	Mounts []sweepMount
}

func (s *containerSweep) inspect(id string) (sweepContainer, error) {
	data, err := s.docker("inspect", "--format", "{{json .}}", id)
	var c sweepContainer
	if err != nil {
		return c, err
	}
	if json.Unmarshal(data, &c) != nil || !sweepID.MatchString(c.ID) || c.ID != id {
		return c, errors.New("invalid container inspection")
	}
	return c, nil
}
func (c sweepContainer) owned() bool {
	return strings.HasPrefix(c.Name, "/") && sweepContainerName.MatchString(strings.TrimPrefix(c.Name, "/")) && c.Config.Labels[containerOwnerLabel] == containerOwnerValue && c.Config.Image == containerGoImage
}
func (s *containerSweep) live(c sweepContainer) (bool, error) {
	if c.Created.IsZero() {
		return false, errors.New("missing container creation time")
	}
	if s.now().Sub(c.Created) < sweepGrace {
		return true, nil
	}
	procs, err := s.processes.list()
	if err != nil {
		return false, err
	}
	for _, p := range procs {
		if p.uid == s.uid() && dockerClientName(p.command) == strings.TrimPrefix(c.Name, "/") {
			return true, nil
		}
	}
	for _, m := range c.Mounts {
		if m.Type != "bind" || m.Destination != "/work" || filepath.Base(m.Source) != "tree" {
			continue
		}
		dir := filepath.Dir(m.Source)
		if s.copyRoot == nil {
			return false, errors.New("copy parent unavailable")
		}
		if !sweepCopyName.MatchString(filepath.Base(dir)) {
			continue
		}
		real, err := filepath.EvalSymlinks(filepath.Dir(dir))
		if err != nil {
			return false, err
		}
		if real != s.resolvedParent {
			continue
		}
		f, _, live, err := s.openCopy(int(s.copyRoot.Fd()), filepath.Base(dir))
		if f != nil {
			_ = f.Close()
		}
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || live {
			return live, err
		}
	}
	return false, nil
}
func (s *containerSweep) absent(id string) bool {
	ids, err := s.ids(false)
	if err != nil {
		return false
	}
	for _, found := range ids {
		if id == found {
			return false
		}
	}
	return true
}
func (s *containerSweep) containers() {
	ids, err := s.ids(true)
	if err != nil {
		s.fail("listing containers")
		return
	}
	for _, id := range ids {
		c, err := s.inspect(id)
		if err != nil {
			if !s.absent(id) {
				s.fail("inspecting container")
			}
			continue
		}
		if !c.owned() {
			continue
		}
		live, err := s.live(c)
		if err != nil {
			s.unreclaimed("container", id)
			continue
		}
		if live {
			s.line("skipped (in use) container %s", id)
			continue
		}
		s.line("leftover container %s %s", id, strings.TrimPrefix(c.Name, "/"))
		if s.dry {
			s.fail("detecting leftover container")
			continue
		}
		// Names are reusable. Revalidate the full identity and liveness, then remove
		// by immutable ID only. A disappearing candidate needs no removal.
		again, err := s.inspect(id)
		if err != nil {
			if !s.absent(id) {
				s.unreclaimed("container", id)
			}
			continue
		}
		if !again.owned() {
			continue
		}
		live, err = s.live(again)
		if err != nil {
			s.unreclaimed("container", id)
			continue
		}
		if live {
			s.line("skipped (in use) container %s", id)
			continue
		}
		if _, err := s.docker("rm", "-f", id); err != nil || !s.absent(id) {
			s.unreclaimed("container", id)
			continue
		}
		s.line("reclaimed container %s", id)
	}
}
