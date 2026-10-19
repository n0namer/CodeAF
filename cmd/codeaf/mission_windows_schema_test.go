//go:build windows

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// These cases keep incomplete provider contracts from passing the wire check.
func TestMissionWindowsEditSchema(t *testing.T) {
	cases := []struct {
		name string
		wire string
		want bool
	}{
		{"null", `null`, false},
		{"empty", `{}`, false},
		{"null edits", `{"edits":null}`, false},
		{"scalar edits", `{"edits":"array"}`, false},
		{"wrong container", `{"edits":{"type":"object"}}`, false},
		{"missing items", `{"edits":{"type":"array"}}`, false},
		{"null items", `{"edits":{"type":"array","items":null}}`, false},
		{"wrong item type", `{"edits":{"type":"array","items":{"type":"string"}}}`, false},
		{"missing properties", `{"edits":{"type":"array","items":{"type":"object"}}}`, false},
		{"null properties", `{"edits":{"type":"array","items":{"type":"object","properties":null}}}`, false},
		{"retired field names", `{"edits":{"type":"array","items":{"type":"object","properties":{"oldText":{"type":"string"},"newText":{"type":"string"}},"required":["oldText","newText"]}}}`, false},
		{"wrong old type", `{"edits":{"type":"array","items":{"type":"object","properties":{"old":{"type":"number"},"new":{"type":"string"}},"required":["old","new"]}}}`, false},
		{"missing new", `{"edits":{"type":"array","items":{"type":"object","properties":{"old":{"type":"string"}},"required":["old","new"]}}}`, false},
		{"missing required", `{"edits":{"type":"array","items":{"type":"object","properties":{"old":{"type":"string"},"new":{"type":"string"}}}}}`, false},
		{"null required", `{"edits":{"type":"array","items":{"type":"object","properties":{"old":{"type":"string"},"new":{"type":"string"}},"required":null}}}`, false},
		{"empty required", `{"edits":{"type":"array","items":{"type":"object","properties":{"old":{"type":"string"},"new":{"type":"string"}},"required":[]}}}`, false},
		{"only old required", `{"edits":{"type":"array","items":{"type":"object","properties":{"old":{"type":"string"},"new":{"type":"string"}},"required":["old"]}}}`, false},
		{"only new required", `{"edits":{"type":"array","items":{"type":"object","properties":{"old":{"type":"string"},"new":{"type":"string"}},"required":["new"]}}}`, false},
		{"scalar required", `{"edits":{"type":"array","items":{"type":"object","properties":{"old":{"type":"string"},"new":{"type":"string"}},"required":"old,new"}}}`, false},
		{"non-string required entries", `{"edits":{"type":"array","items":{"type":"object","properties":{"old":{"type":"string"},"new":{"type":"string"}},"required":[null,42,{},[],"old"]}}}`, false},
		{"null old property", `{"edits":{"type":"array","items":{"type":"object","properties":{"old":null,"new":{"type":"string"}},"required":["old","new"]}}}`, false},
		{"wrong new type", `{"edits":{"type":"array","items":{"type":"object","properties":{"old":{"type":"string"},"new":{"type":"number"}},"required":["old","new"]}}}`, false},
		{"valid", `{"edits":{"type":"array","items":{"type":"object","properties":{"old":{"type":"string"},"new":{"type":"string"}},"required":["old","new"]}}}`, true},
		{"reordered required", `{"edits":{"type":"array","items":{"type":"object","properties":{"old":{"type":"string"},"new":{"type":"string"}},"required":["new","old"]}}}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var properties map[string]any
			if err := json.Unmarshal([]byte(tc.wire), &properties); err != nil {
				t.Fatal(err)
			}
			if got := hasEditArraySchema(properties, "edits"); got != tc.want {
				t.Fatalf("hasEditArraySchema(%s) = %v, want %v", tc.wire, got, tc.want)
			}
		})
	}
}

func TestMissionWindowsPathSchema(t *testing.T) {
	for _, wire := range []string{`null`, `{}`, `{"path":null}`, `{"path":"string"}`, `{"path":{"type":"number"}}`} {
		t.Run(wire, func(t *testing.T) {
			var properties map[string]any
			if err := json.Unmarshal([]byte(wire), &properties); err != nil {
				t.Fatal(err)
			}
			if hasStringSchemaProperty(properties, "path") {
				t.Fatalf("accepted malformed path schema: %s", wire)
			}
		})
	}
	if !hasStringSchemaProperty(map[string]any{"path": map[string]any{"type": "string"}}, "path") {
		t.Fatal("rejected string path schema")
	}
}

func TestMissionWindowsIsolatedEnv(t *testing.T) {
	if got := isolatedMissionEnv(nil, nil); len(got) != 0 {
		t.Fatalf("empty environment = %v", got)
	}
	inherited := []string{"Path=first", "PATH=second", "https_proxy=secret", "KEEP=a=b", "malformed"}
	got := isolatedMissionEnv(inherited, map[string]string{"path": "local", "HTTPS_PROXY": ""})
	values := make(map[string]string)
	for _, entry := range got {
		key, value, found := strings.Cut(entry, "=")
		if !found {
			t.Fatalf("malformed entry survived: %q", entry)
		}
		key = strings.ToUpper(key)
		if _, duplicate := values[key]; duplicate {
			t.Fatalf("duplicate Windows environment key: %s", key)
		}
		values[key] = value
	}
	if len(values) != 3 || values["PATH"] != "local" || values["HTTPS_PROXY"] != "" || values["KEEP"] != "a=b" {
		t.Fatalf("environment overrides were not isolated: %v", values)
	}
	if inherited[0] != "Path=first" || inherited[2] != "https_proxy=secret" {
		t.Fatal("inherited environment was mutated")
	}
}
