package fbshared

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestInsertFlatFields(t *testing.T) {
	raw := json.RawMessage(`{"title":"x","id":9007199254740993123}`)
	extra := map[string]any{"proto_type": "hotlist", "cycle_count": 3}

	out, err := InsertFlatFields(raw, extra)
	if err != nil {
		t.Fatalf("InsertFlatFields: %v", err)
	}
	if string(out) == string(raw) {
		t.Fatalf("expected fields inserted, got same bytes")
	}

	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if m["proto_type"] != "hotlist" {
		t.Errorf("proto_type = %v, want hotlist", m["proto_type"])
	}
	if m["cycle_count"] != json.Number("3") {
		t.Errorf("cycle_count = %v, want 3", m["cycle_count"])
	}
	if m["title"] != "x" {
		t.Errorf("title lost: %v", m["title"])
	}
	if got := m["id"]; got != json.Number("9007199254740993123") {
		t.Errorf("big int not preserved: %v", got)
	}
}

func TestInsertFlatFieldsNoOverwrite(t *testing.T) {
	raw := json.RawMessage(`{"kind":"original","cycle_count":9}`)
	out, err := InsertFlatFields(raw, map[string]any{"kind": "hotlist", "cycle_count": 3})
	if err != nil {
		t.Fatalf("InsertFlatFields: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if m["kind"] != "original" {
		t.Errorf("existing key overwritten: %v", m["kind"])
	}
	if got := m["cycle_count"]; got != float64(9) {
		t.Errorf("cycle_count overwritten: %v", got)
	}
}

func TestInsertFlatFieldsBadInput(t *testing.T) {
	if _, err := InsertFlatFields(json.RawMessage(`not json`), map[string]any{}); err == nil {
		t.Fatalf("expected error on bad input")
	}
}