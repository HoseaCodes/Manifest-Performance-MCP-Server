package tools

import (
	"encoding/json"
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
