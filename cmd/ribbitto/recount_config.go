package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
)

type recountConfig struct {
	enabled                                    bool
	poolMax, workers, maxConnections, reserved int64
	external, processes, headroom, budget      int64
	registrationLimit                          int
}

// recountConfiguration validates capacity before opt-in delivery is composed.
// Bound inputs to int32 (pgx's pool limit); int64 arithmetic avoids overflow.
func recountConfiguration(streamCap int) (recountConfig, error) {
	c := recountConfig{registrationLimit: streamCap}
	switch os.Getenv("RIBBITTO_ASYNC_RECOUNTS") {
	case "", "off":
		return c, nil // Stray budget settings have no effect while disabled.
	case "on":
		c.enabled = true
	default:
		return c, fmt.Errorf("RIBBITTO_ASYNC_RECOUNTS must be on, off or empty")
	}
	var violations []error
	parsed := make(map[string]bool)
	for _, input := range []struct {
		name string
		dest *int64
		zero bool
	}{
		{"RIBBITTO_POOL_MAX_CONNS", &c.poolMax, false},
		{"RIBBITTO_RECOUNT_WORKERS", &c.workers, false},
		{"RIBBITTO_DB_MAX_CONNECTIONS", &c.maxConnections, false},
		{"RIBBITTO_DB_RESERVED_CONNECTIONS", &c.reserved, true},
		{"RIBBITTO_DB_EXTERNAL_CONNECTIONS", &c.external, true},
		{"RIBBITTO_DB_PROCESSES", &c.processes, false},
		{"RIBBITTO_DB_NORMAL_HEADROOM", &c.headroom, true},
	} {
		value, err := strconv.ParseInt(os.Getenv(input.name), 10, 32)
		if err != nil {
			violations = append(violations, fmt.Errorf("%s is required and must be a 32-bit integer", input.name))
			continue
		}
		*input.dest = value
		parsed[input.name] = true
		if value < 0 || (!input.zero && value == 0) {
			rule := "positive"
			if input.zero {
				rule = "non-negative"
			}
			violations = append(violations, fmt.Errorf("%s must be %s", input.name, rule))
		}
	}
	if parsed["RIBBITTO_DB_MAX_CONNECTIONS"] && parsed["RIBBITTO_DB_RESERVED_CONNECTIONS"] && parsed["RIBBITTO_DB_EXTERNAL_CONNECTIONS"] && c.processes > 0 {
		available := c.maxConnections - c.reserved - c.external
		c.budget = available / c.processes
		if available < 0 && available%c.processes != 0 {
			c.budget-- // Go truncates toward zero; the contract requires floor.
		}
		if parsed["RIBBITTO_POOL_MAX_CONNS"] && c.poolMax > c.budget {
			violations = append(violations, fmt.Errorf("RIBBITTO_POOL_MAX_CONNS (C=%d) must be <= B=floor((M-R-E)/P)=%d", c.poolMax, c.budget))
		}
	}
	if parsed["RIBBITTO_POOL_MAX_CONNS"] && parsed["RIBBITTO_RECOUNT_WORKERS"] && parsed["RIBBITTO_DB_NORMAL_HEADROOM"] && c.workers > c.poolMax-c.headroom {
		violations = append(violations, fmt.Errorf("RIBBITTO_RECOUNT_WORKERS (W=%d) must be <= C-H=%d", c.workers, c.poolMax-c.headroom))
	}
	return c, errors.Join(violations...)
}

// computationLimit reserves normal-work headroom even without the async budgets.
func (c recountConfig) computationLimit(poolMax int32) int {
	if c.enabled {
		return int(c.workers)
	} // W <= C-H was validated before opening.
	return max(1, int(poolMax)/2)
}
