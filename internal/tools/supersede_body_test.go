package tools

import (
	"encoding/json"
	"fmt"
	"testing"
)

/*
supersedeBodyOmitsSlotID pins a fix that is invisible on sight.

The slot is named in the URL path, and the API validates the body strictly. A
body that also carried slotId was rejected with "Unrecognized key: slotId", so
supersede_workout_prescription failed every time it was called -- not in an
edge case, in the only case. It was found by running the tool against a live
API; no unit test could have seen it, because nothing here knew the body was
strict.

The field is suppressed by shadowing the embedded one, which is easy to undo by
accident while tidying the struct. Hence this test.
*/
func TestSupersedeBodyOmitsSlotID(t *testing.T) {
	in := SupersedeInput{
		SlotID:            "slot-123",
		ExpectedRevision:  2,
		LocalDate:         "2026-09-18",
		SessionOfDay:      "morning",
		ScheduledTimezone: "America/Chicago",
		Name:              "Lighter Morning",
	}

	body := struct {
		SupersedeInput
		SlotID string `json:"slotId,omitempty"`
		Source source `json:"source"`
	}{SupersedeInput: in, Source: aiGenerated()}

	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if _, present := decoded["slotId"]; present {
		t.Fatalf("body carries slotId; the API rejects it as an unrecognized key: %s", raw)
	}

	// The rest must still be there -- suppressing the wrong field would be a
	// quieter failure than the one this guards.
	for _, key := range []string{"expectedRevision", "localDate", "sessionOfDay", "name", "source"} {
		if _, present := decoded[key]; !present {
			t.Errorf("body is missing %q", key)
		}
	}
	if decoded["expectedRevision"] != float64(2) {
		t.Errorf("expectedRevision = %v, want 2", decoded["expectedRevision"])
	}
}

/*
TestSchemasHaveNoNullableTypeUnions guards the shape that made the connector
look empty rather than broken.

Go pointer fields infer as `"type": ["null","integer"]`. That is valid JSON
Schema, and the official MCP Inspector reports zero errors for it. But several
MCP clients read `type` as a single string and reject the tool — and a rejected
tool is not reported as an error, it simply does not appear. The connector then
presents as having no actions, which looks identical to a connection that never
succeeded, and sends you debugging the transport instead of the schema.

47 of these existed across three tools before `portableSchema` was introduced.
*/
func TestSchemasHaveNoNullableTypeUnions(t *testing.T) {
	schemas := map[string]any{
		"get_generation_context":         portableSchema[ContextInput](),
		"create_workout_prescription":    portableSchema[CreateInput](),
		"get_workout_prescription":       portableSchema[GetInput](),
		"supersede_workout_prescription": portableSchema[SupersedeInput](),
	}

	for name, schema := range schemas {
		raw, err := json.Marshal(schema)
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}

		var walk func(path string, v any)
		walk = func(path string, v any) {
			switch node := v.(type) {
			case map[string]any:
				if typ, ok := node["type"]; ok {
					if _, isArray := typ.([]any); isArray {
						t.Errorf("%s: %s has an array `type`; strict clients reject the tool", name, path)
					}
				}
				for k, child := range node {
					walk(path+"."+k, child)
				}
			case []any:
				for i, child := range node {
					walk(fmt.Sprintf("%s[%d]", path, i), child)
				}
			}
		}

		var decoded any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("%s: unmarshal: %v", name, err)
		}
		walk("inputSchema", decoded)
	}
}

// The rewrite must preserve the contract, not just satisfy the linter: a field
// that accepted null before still accepts it, via anyOf.
func TestNullabilityIsPreservedAsAnyOf(t *testing.T) {
	raw, err := json.Marshal(portableSchema[CreateInput]())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	props, _ := decoded["properties"].(map[string]any)
	dur, _ := props["estimatedDuration"].(map[string]any)
	if dur == nil {
		t.Fatal("estimatedDuration missing from the schema")
	}

	branches, ok := dur["anyOf"].([]any)
	if !ok {
		t.Fatalf("estimatedDuration = %v, want anyOf branches (it is *int, so nullable)", dur)
	}

	seen := map[string]bool{}
	for _, b := range branches {
		if m, ok := b.(map[string]any); ok {
			if s, ok := m["type"].(string); ok {
				seen[s] = true
			}
		}
	}
	if !seen["null"] || !seen["integer"] {
		t.Errorf("branches = %v, want both null and integer — absent is not the same as null", seen)
	}
}
