package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type agyCheckSavedFile struct {
	root, path       string
	data             []byte
	mode             os.FileMode
	present, touched bool
	parentPresent    bool
}

// Only these three marked-home targets are exceptions to isolation confinement.
// No account-wide configuration is inspected, named, or hashed here.
func agyCheckPlace(m agyCheckMarker, r agyCheckRowDirs, config agyCheckConfigFiles, fixtures map[string][]byte, f agyCheckFiles, body func() error) (err error) {
	if m.AccountHome != "" && agyCheckOverlap(m.Home, m.AccountHome) {
		return errors.New("placement home overlaps account home")
	}
	if err := r.validate(m.Isolation, f); err != nil {
		return err
	}
	if !agyCheckCleanPath(m.Home) || !implementRunInside(m.Home, m.Isolation) || strings.EqualFold(m.Home, m.Isolation) {
		return errors.New("invalid placement home")
	}
	files := []agyCheckSavedFile{
		{root: m.Home, path: filepath.Join(m.Home, ".gemini/config/hooks.json"), data: config.Hooks},
		{root: m.Home, path: filepath.Join(m.Home, ".gemini/config/mcp_config.json"), data: config.MCP},
		{root: m.Home, path: filepath.Join(m.Home, ".gemini/antigravity-cli/settings.json"), data: config.Settings},
	}
	var locations []string
	for location := range fixtures {
		if !agyCheckConfigLocation(location) {
			return errors.New("invalid workspace fixture location")
		}
		locations = append(locations, location)
	}
	sort.Strings(locations)
	for _, location := range locations {
		files = append(files, agyCheckSavedFile{root: m.Isolation, path: filepath.Join(r.Workspace, location), data: fixtures[location]})
	}
	writes := make([][]byte, len(files))
	// Snapshot every target before any mutation, including failures on later files.
	for i := range files {
		s := &files[i]
		writes[i] = s.data
		s.data = nil
		if err := f.path(s.root, s.path); err != nil {
			return err
		}
		info, statErr := f.lstat(s.path)
		if errors.Is(statErr, os.ErrNotExist) {
			_, parentErr := f.lstat(filepath.Dir(s.path))
			if parentErr == nil {
				s.parentPresent = true
			} else if !errors.Is(parentErr, os.ErrNotExist) {
				return parentErr
			}
			continue
		}
		if statErr != nil || !info.Mode().IsRegular() {
			return errors.New("configuration target must be a regular file")
		}
		if i >= 3 {
			return errors.New("workspace fixture already exists")
		}
		data, readErr := f.readInside(s.root, s.path, 1<<20)
		if readErr != nil || len(data) > 1<<20 {
			return errors.New("configuration snapshot exceeds cap or is unreadable")
		}
		s.data, s.mode, s.present = data, info.Mode(), true
	}
	var created []string
	defer func() {
		if recover() != nil {
			err = errors.Join(err, errors.New("agy-check placement panic"))
		}
		var restoreErr error
		for i := len(files) - 1; i >= 0; i-- {
			s := files[i]
			if !s.touched {
				continue
			}
			restore := func() error {
				if err := f.path(s.root, s.path); err != nil {
					return err
				}
				if info, e := f.lstat(s.path); e == nil {
					if !info.Mode().IsRegular() {
						return errors.New("restoration target changed type")
					}
					if err := f.remove(s.path); err != nil {
						return err
					}
				} else if !errors.Is(e, os.ErrNotExist) {
					return e
				}
				if s.present {
					if err := f.write(s.root, s.path, s.data, true, s.mode); err != nil {
						return err
					}
					data, e := f.readInside(s.root, s.path, 1<<20)
					info, statErr := f.lstat(s.path)
					if e != nil || statErr != nil || !bytes.Equal(data, s.data) || info.Mode() != s.mode {
						return errors.New("restoration verification failed")
					}
				} else {
					if _, e := f.lstat(s.path); !errors.Is(e, os.ErrNotExist) {
						return errors.New("restoration left an absent target")
					}
					// An absent leaf alone cannot confirm restoration if agy also
					// removed or replaced its pre-existing parent directory.
					if s.parentPresent {
						parent, e := f.lstat(filepath.Dir(s.path))
						if e != nil || !parent.IsDir() {
							return errors.New("restoration verification failed")
						}
					}
				}
				return nil
			}
			restoreErr = errors.Join(restoreErr, restore())
		}
		for i := len(created) - 1; i >= 0; i-- {
			root := m.Home
			if implementRunInside(m.Isolation, created[i]) {
				root = m.Isolation
			}
			if e := f.path(root, created[i]); e != nil {
				restoreErr = errors.Join(restoreErr, e)
				continue
			}
			restoreErr = errors.Join(restoreErr, f.remove(created[i]))
		}
		if restoreErr != nil {
			err = errors.Join(err, errors.New("restoring agy-check configuration failed"), restoreErr)
		}
	}()
	for i := range files {
		s := &files[i]
		var missing []string
		for parent := filepath.Dir(s.path); parent != s.root; parent = filepath.Dir(parent) {
			if _, e := f.lstat(parent); errors.Is(e, os.ErrNotExist) {
				missing = append(missing, parent)
			} else if e != nil {
				return e
			}
		}
		for j := len(missing) - 1; j >= 0; j-- {
			if err := f.path(s.root, missing[j]); err != nil {
				return err
			}
			if err := f.mkdir(missing[j], 0o700); err != nil {
				return err
			}
			created = append(created, missing[j])
		}
		s.touched = true
		if s.present {
			if err := f.path(s.root, s.path); err != nil {
				return err
			}
			if err := f.remove(s.path); err != nil {
				return err
			}
		}
		if err := f.write(s.root, s.path, writes[i], true, 0o600); err != nil {
			return err
		}
	}
	return body()
}
