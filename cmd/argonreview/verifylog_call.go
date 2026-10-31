package main

import (
	"bytes"
	"encoding/json"
)

// Name decoding is only a routing hint. Strict call and argument validation
// still run before selection; malformed calls must count and emit an audit.
func verifyLogCallNamed(raw []byte) bool {
	var hint struct{ Name json.RawMessage }
	var name string
	return json.Unmarshal(raw, &hint) == nil && json.Unmarshal(hint.Name, &name) == nil && name == verifyLogToolName
}

func (s *verifyLogStore) verifyLogCall(raw []byte, id json.RawMessage) (verifyLogReply, string) {
	fields, err := verifyObject(raw)
	valid := err == nil
	for key := range fields {
		if key != "name" && key != "arguments" && key != "_meta" {
			valid = false
		}
	}
	var name string
	if json.Unmarshal(fields["name"], &name) != nil || name != verifyLogToolName {
		valid = false
	}
	if meta, ok := fields["_meta"]; ok {
		if _, err := verifyObject(meta); err != nil {
			valid = false
		}
	}
	if !valid {
		return s.read(verifyLogArguments{}, id, verifyLogMalformed)
	}
	return s.readRaw(fields["arguments"], id)
}

// The envelope parser recursively rejects duplicate params keys before tool
// dispatch. Replace only the params value to reuse its strict envelope checks,
// then restore the original bytes for the store's malformed-call accounting.
// Duplicate envelope keys, invalid IDs and invalid JSON remain envelope errors.
func verifyLogRecoverRequest(line []byte) (verifyRequest, bool) {
	var hint struct{ Params json.RawMessage }
	if json.Unmarshal(line, &hint) != nil || !verifyLogCallNamed(hint.Params) {
		return verifyRequest{}, false
	}
	envelope := bytes.Replace(line, hint.Params, []byte(`{}`), 1)
	req, err := parseVerifyRequest(envelope)
	if err != nil || req.method != "tools/call" {
		return verifyRequest{}, false
	}
	req.params = hint.Params
	return req, true
}
