package static

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"
)

// Scripts resolves declared page entries to their generated bundles.
// Its contents are immutable after construction.
type Scripts struct {
	urls    map[string]string
	bundles map[string]bool
	maps    map[string]bool
}

// NewScripts validates the runtime manifest against the page entry registry and
// assets. Call it at startup so incomplete generations cannot serve pages.
func NewScripts(assets fs.FS) (*Scripts, error) {
	data, err := fs.ReadFile(assets, "dist/manifest.json")
	if err != nil {
		return nil, fmt.Errorf("reading script manifest: %w", err)
	}
	var manifest struct {
		Entries map[string]struct {
			File   string
			SHA256 string
			Map    string
		}
		Previous []map[string]struct {
			File string
			Map  string
		}
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("decoding script manifest: %w", err)
	}
	// This is the single declaration of converted page entries; tools/webbuild
	// parses it from NewScripts, so keep its name and string slice literal.
	names := []string{
		"message-time",
		"composer",
	}
	scripts := &Scripts{
		urls:    make(map[string]string, len(names)),
		bundles: make(map[string]bool, len(names)),
		maps:    make(map[string]bool, len(names)),
	}
	for _, name := range names {
		entry, ok := manifest.Entries[name]
		if !ok {
			return nil, fmt.Errorf("script manifest missing declared entry %q", name)
		}
		if !regexp.MustCompile(`^` + regexp.QuoteMeta(name) + `-[A-Z0-9]{8}\.js$`).MatchString(entry.File) {
			return nil, fmt.Errorf("invalid bundle filename %q", entry.File)
		}
		bundle, err := fs.ReadFile(assets, "dist/"+entry.File)
		if err != nil {
			return nil, fmt.Errorf("reading script bundle %q: %w", name, err)
		}
		if fmt.Sprintf("%x", sha256.Sum256(bundle)) != entry.SHA256 {
			return nil, fmt.Errorf("script bundle %q hash mismatch", name)
		}
		scripts.urls[name] = "/static/dist/" + entry.File
		scripts.bundles["dist/"+entry.File] = true
		if entry.Map != "" {
			if entry.Map != entry.File+".map" {
				return nil, fmt.Errorf("invalid source map filename %q", entry.Map)
			}
			if _, err := fs.ReadFile(assets, "dist/"+entry.Map); err != nil {
				return nil, fmt.Errorf("reading source map %q: %w", name, err)
			}
			scripts.maps["dist/"+entry.Map] = true
		}
		delete(manifest.Entries, name)
	}
	for name := range manifest.Entries {
		return nil, fmt.Errorf("script manifest names unknown entry %q", name)
	}
	for _, prev := range manifest.Previous {
		for name, entry := range prev {
			if scripts.urls[name] == "" {
				return nil, fmt.Errorf("script manifest names unknown entry %q", name)
			}
			if !regexp.MustCompile(`^` + regexp.QuoteMeta(name) + `-[A-Z0-9]{8}\.js$`).MatchString(entry.File) {
				return nil, fmt.Errorf("invalid bundle filename %q", entry.File)
			}
			scripts.bundles["dist/"+entry.File] = true
			if entry.Map != "" {
				if entry.Map != entry.File+".map" {
					return nil, fmt.Errorf("invalid source map filename %q", entry.Map)
				}
				scripts.maps["dist/"+entry.Map] = true
			}
		}
	}
	return scripts, nil
}

// URL returns the generated URL, rejecting names not declared by the registry.
func (s *Scripts) URL(name string) (string, error) {
	if url := s.urls[name]; url != "" {
		return url, nil
	}
	return "", fmt.Errorf("undeclared page script %q", name)
}

// IsBundle reports whether a static-relative path is a registered bundle.
func (s *Scripts) IsBundle(path string) bool {
	return s.bundles[path]
}

// IsMap reports whether a static-relative path is a registered development source map.
func (s *Scripts) IsMap(path string) bool {
	return s.maps[path]
}

type scriptsKey struct{}

// WithScripts supplies the startup-validated registry to page rendering.
func WithScripts(ctx context.Context, scripts *Scripts) context.Context {
	return context.WithValue(ctx, scriptsKey{}, scripts)
}

// ScriptURL resolves a logical entry in the rendering context. Direct component
// renders use the same embedded registry without package-level mutable state.
func ScriptURL(ctx context.Context, name string) (string, error) {
	scripts, ok := ctx.Value(scriptsKey{}).(*Scripts)
	if !ok {
		// Rebuild and validate per render only for direct component renders and
		// tests. Production must not rely on this fallback: pageRenderer.render
		// supplies the startup-validated registry.
		var err error
		scripts, err = NewScripts(FS())
		if err != nil {
			return "", err
		}
	}
	return scripts.URL(name)
}
