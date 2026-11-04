package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const productionActive = "sourcemaps-production-v1 active"
const productionRetired = "sourcemaps-production-v1 retired "

func keepProduction(body string, now time.Time) (bool, error) {
	switch {
	case body == "":
		return false, nil
	case body == productionActive:
		return true, nil
	case strings.HasPrefix(body, productionRetired):
		s := strings.TrimPrefix(body, productionRetired)
		retired, err := time.Parse(time.RFC3339, s)
		if err == nil && retired.UTC().Format(time.RFC3339) == s {
			if retired.After(now) {
				return true, fmt.Errorf("retirement time is in the future; retention refused")
			}
			return now.Sub(retired) < 90*24*time.Hour, nil
		}
	}
	return true, fmt.Errorf("invalid production marker; retention refused")
}

func runProduction(ctx context.Context, cfg config, name, state string) error {
	p, ok := parseReleaseName(name)
	if !ok || cfg.now.IsZero() || cfg.token == "" || p.Start.After(cfg.now.Add(24*time.Hour)) || (state != "active" && state != "retired") {
		return fmt.Errorf("invalid production marker arguments")
	}
	c := &apiClient{cfg: cfg}
	var r ghRelease
	if code, _, err := c.do(ctx, true, "GET", fmt.Sprintf("/repos/%s/releases/tags/%s", cfg.storeRepo, name), "", nil, &r); err != nil || code != 200 || r.ID <= 0 || r.Name != name || r.TagName != name || r.Draft || r.Prerelease {
		return fmt.Errorf("reading production release: status %d (%v)", code, err)
	}
	if _, err := keepProduction(r.Body, cfg.now); err != nil {
		return fmt.Errorf("release %d tag %s: %w", r.ID, safeQuote(r.TagName), err)
	}
	body := productionActive
	if state == "retired" {
		if r.Body == "" {
			return fmt.Errorf("cannot retire an unmarked release")
		}
		body = r.Body
		if body == productionActive {
			body = productionRetired + cfg.now.UTC().Format(time.RFC3339)
		}
	}
	data, _ := json.Marshal(map[string]string{"body": body})
	var updated ghRelease
	if code, _, err := c.do(ctx, true, "PATCH", fmt.Sprintf("/repos/%s/releases/%d", cfg.storeRepo, r.ID), "", data, &updated); err != nil || code != 200 || updated.ID != r.ID || updated.Name != name || updated.TagName != name || updated.Draft || updated.Prerelease || updated.Body != body {
		return fmt.Errorf("writing production marker: status %d (%v)", code, err)
	}
	logf(cfg.stdout, "production release %d tag %s: %s\n", r.ID, safeQuote(r.TagName), state)
	return nil
}
