package main

import (
	"os"
	"strings"
	"testing"
)

func recountInputs(t *testing.T) map[string]string {
	t.Helper()
	inputs := map[string]string{
		"RIBBITTO_POOL_MAX_CONNS": "36", "RIBBITTO_RECOUNT_WORKERS": "26",
		"RIBBITTO_DB_MAX_CONNECTIONS": "100", "RIBBITTO_DB_RESERVED_CONNECTIONS": "3",
		"RIBBITTO_DB_EXTERNAL_CONNECTIONS": "20", "RIBBITTO_DB_PROCESSES": "2",
		"RIBBITTO_DB_NORMAL_HEADROOM": "10",
	}
	t.Setenv("RIBBITTO_ASYNC_RECOUNTS", "on")
	for name, value := range inputs {
		t.Setenv(name, value)
	}
	return inputs
}

func TestRecountBudget(t *testing.T) {
	for _, tt := range []struct {
		name, env, value, wantError string
		budget, poolMax, workers    int64
	}{
		{"reference", "RIBBITTO_POOL_MAX_CONNS", "36", "", 38, 36, 26},
		{"pool boundary", "RIBBITTO_POOL_MAX_CONNS", "38", "", 38, 38, 26},
		{"pool excess", "RIBBITTO_POOL_MAX_CONNS", "39", "C=39", 38, 39, 26},
		{"worker excess", "RIBBITTO_RECOUNT_WORKERS", "27", "C-H=26", 38, 36, 27},
		{"no budget", "RIBBITTO_DB_MAX_CONNECTIONS", "23", "B=floor((M-R-E)/P)=0", 0, 36, 26},
		{"negative floor", "RIBBITTO_DB_MAX_CONNECTIONS", "20", "B=floor((M-R-E)/P)=-2", -2, 36, 26},
		{"no headroom", "RIBBITTO_DB_NORMAL_HEADROOM", "36", "C-H=0", 38, 36, 26},
		{"zero reserve", "RIBBITTO_DB_RESERVED_CONNECTIONS", "0", "", 40, 36, 26},
		{"zero external", "RIBBITTO_DB_EXTERNAL_CONNECTIONS", "0", "", 48, 36, 26},
		{"zero headroom", "RIBBITTO_DB_NORMAL_HEADROOM", "0", "", 38, 36, 26},
	} {
		t.Run(tt.name, func(t *testing.T) {
			recountInputs(t)
			t.Setenv(tt.env, tt.value)
			got, err := recountConfiguration(7000)
			if got.budget != tt.budget || !got.enabled || got.registrationLimit != 7000 {
				t.Fatalf("configuration = %+v; want budget=%d, enabled, registrationLimit=7000", got, tt.budget)
			}
			if tt.wantError == "" {
				if err != nil || got.poolMax != tt.poolMax || got.workers != tt.workers {
					t.Fatalf("explicit capacity not preserved: %+v, %v", got, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("error = %v; want %q", err, tt.wantError)
			}
		})
	}
}

func TestRecountInvalidInputs(t *testing.T) {
	inputs := recountInputs(t)
	t.Setenv("RIBBITTO_MAX_STREAMS", "5000")
	for name := range inputs {
		for _, value := range []string{"unset", "", "bad", "1.5", " 1", "2147483648", "-1", "0"} {
			if value == "0" && (strings.Contains(name, "RESERVED") || strings.Contains(name, "EXTERNAL") || strings.Contains(name, "HEADROOM")) {
				continue
			}
			t.Run(name+"/"+value, func(t *testing.T) {
				t.Setenv(name, value)
				if value == "unset" {
					if err := os.Unsetenv(name); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := recountConfiguration(5000); err == nil || !strings.Contains(err.Error(), name) {
					t.Fatalf("invalid %s=%q accepted: %v", name, value, err)
				}
				if err := serve(t.Context(), ""); err == nil || !strings.Contains(err.Error(), name) {
					t.Fatalf("invalid budget did not prevent startup: %v", err)
				}
			})
		}
	}
	for name := range inputs {
		t.Setenv(name, "")
	}
	_, err := recountConfiguration(5000)
	for name := range inputs {
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("missing input not reported: %s: %v", name, err)
		}
	}
	recountInputs(t)
	t.Setenv("RIBBITTO_POOL_MAX_CONNS", "39")
	t.Setenv("RIBBITTO_RECOUNT_WORKERS", "30")
	_, err = recountConfiguration(5000)
	if err == nil || !strings.Contains(err.Error(), "C=39") || !strings.Contains(err.Error(), "C-H=29") {
		t.Fatalf("must report both violated budget rules: %v", err)
	}
}

func TestRecountSwitch(t *testing.T) {
	for _, value := range []string{"unset", "", "off", "on", "ON", "true", " on"} {
		t.Run(value, func(t *testing.T) {
			for name := range recountInputs(t) {
				t.Setenv(name, "stray-invalid")
			}
			t.Setenv("RIBBITTO_ASYNC_RECOUNTS", value)
			if value == "unset" {
				if err := os.Unsetenv("RIBBITTO_ASYNC_RECOUNTS"); err != nil {
					t.Fatal(err)
				}
			}
			got, err := recountConfiguration(123)
			if value == "unset" || value == "" || value == "off" {
				if err != nil || got != (recountConfig{registrationLimit: 123}) {
					t.Fatalf("off must ignore stray inputs: %+v, %v", got, err)
				}
			} else if err == nil || (value != "on" && !strings.Contains(err.Error(), "RIBBITTO_ASYNC_RECOUNTS")) {
				t.Fatalf("invalid configuration accepted or wrong error: %v", err)
			}
		})
	}
}
