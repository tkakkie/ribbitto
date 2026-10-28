package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	verifyToolName        = "verify"
	verifyCheckProperty   = "check"
	verifyPackageProperty = "package"
	verifyRunProperty     = "run"
	verifyBrowserCheck    = "test-browser"
	verifyPackageLimit    = 200
)

// Keep the wire schema and argument parser together so the guard can reuse
// exactly this boundary rather than translating it into another validator.
func verifyCheckNames() []string { return []string{"build", "vet", "test", "generated", "test-db"} }

type verifyArguments struct{ check, pattern, run string }

func parseVerifyArguments(raw []byte) (verifyArguments, error) {
	fields, err := verifyObject(raw)
	if err != nil {
		return verifyArguments{}, fmt.Errorf("reading verify arguments: %w", err)
	}
	var check string
	_ = json.Unmarshal(fields[verifyCheckProperty], &check)
	if check == verifyBrowserCheck {
		if len(fields) != 3 {
			return verifyArguments{}, errors.New("test-browser requires exactly check, package and run")
		}
	} else if len(fields) != 2 {
		return verifyArguments{}, errors.New("verify requires exactly check and package")
	}
	var a verifyArguments
	for key, value := range fields {
		var target *string
		switch key {
		case verifyCheckProperty:
			target = &a.check
		case verifyPackageProperty:
			target = &a.pattern
		case verifyRunProperty:
			target = &a.run
		default:
			return a, errors.New("unknown verify argument")
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) || json.Unmarshal(value, target) != nil {
			return a, errors.New("verify arguments must be strings")
		}
		if len(*target) > verifyPackageLimit {
			return a, errors.New("verify argument exceeds length limit")
		}
	}
	if a.check != verifyBrowserCheck {
		if _, exists := fields[verifyRunProperty]; exists {
			return a, errors.New("run is only allowed for test-browser")
		}
	}
	if err := validateContainerCheck("fixed-worktree", a.check, a.pattern, a.run); err != nil {
		return a, fmt.Errorf("validating verify arguments: %w", err)
	}
	return a, nil
}

func verifyInputSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		verifyCheckProperty:   map[string]any{"type": "string", "enum": append(verifyCheckNames(), verifyBrowserCheck)},
		verifyRunProperty:     map[string]any{"type": "string", "maxLength": 200},
		verifyPackageProperty: map[string]any{"type": "string", "pattern": containerPattern.String(), "maxLength": verifyPackageLimit},
	}, "required": []string{verifyCheckProperty, verifyPackageProperty}, "additionalProperties": false}
}

// Decode keys on the token stream: struct decoding folds case and silently
// replaces duplicate keys, including escaped spellings of the same key.
func verifyJSONValue(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return fmt.Errorf("reading JSON token: %w", err)
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return fmt.Errorf("reading JSON key: %w", err)
			}
			name, ok := key.(string)
			if !ok {
				return errors.New("non-string JSON key")
			}
			if seen[name] {
				return errors.New("duplicate JSON key")
			}
			seen[name] = true
			if err := verifyJSONValue(d); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := verifyJSONValue(d); err != nil {
				return err
			}
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	if _, err := d.Token(); err != nil {
		return fmt.Errorf("ending JSON value: %w", err)
	}
	return nil
}

func verifyObject(raw []byte) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := verifyJSONValue(d); err != nil {
		return nil, err
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing JSON data")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	if token, err := d.Token(); err != nil || token != json.Delim('{') {
		return nil, errors.New("expected JSON object")
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return nil, fmt.Errorf("reading object key: %w", err)
		}
		key, _ := token.(string)
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return nil, fmt.Errorf("reading object value: %w", err)
		}
		fields[key] = value
	}
	return fields, nil
}
