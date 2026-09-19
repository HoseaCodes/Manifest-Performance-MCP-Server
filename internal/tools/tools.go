// Package tools defines the four operations an assistant can perform.
//
// Kept deliberately thin. Every rule that matters — who the athlete is, what a
// scope permits, whether a slot is free, whether a session may be replaced —
// lives behind the API. Re-implementing any of it here would create a second
// copy that drifts, and the copy an assistant can reach is not the one that
// enforces anything.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hoseacodes/manifestfitness/workout-mcp/internal/client"
)

/*
Input types are Go structs so the SDK infers each JSON schema from them. Two
consequences worth having:

  - A field that does not exist here cannot be sent. There is no `status` on
    CreateInput, so an assistant cannot create a session that is already
    complete — enforcement by absence rather than by a check someone may remove.
  - There is no athlete field on any input. The athlete comes from the token's
    `sub`, so there is no parameter to pass one.
*/

type ContextInput struct {
	WorkoutLimit    int      `json:"workoutLimit,omitempty" jsonschema:"recent sessions to include, max 50"`
	PerformanceDays int      `json:"performanceDays,omitempty" jsonschema:"window for per-exercise history in days, max 90"`
	ReadinessDays   int      `json:"readinessDays,omitempty" jsonschema:"window for readiness in days, max 30"`
	Exercises       []string `json:"exercises,omitempty" jsonschema:"movements to return history for, capped at 20"`
	// Pass "evening" when generating an evening session: the response then
	// carries an eveningAdjustment directive computed from the morning's
	// outcome. Work out that adjustment yourself and it varies between runs.
	SessionOfDay string `json:"sessionOfDay,omitempty" jsonschema:"morning or evening; evening returns the adjustment directive"`
}

// Section and Item mirror the API's prescription shape.
type Quantity struct {
	Kind        string   `json:"kind" jsonschema:"one of fixed range duration distance interval work_rest amrap text"`
	Value       *float64 `json:"value,omitempty"`
	Min         *float64 `json:"min,omitempty"`
	Max         *float64 `json:"max,omitempty"`
	Unit        string   `json:"unit,omitempty"`
	PerSide     bool     `json:"perSide,omitempty"`
	Rounds      *int     `json:"rounds,omitempty"`
	WorkSeconds *int     `json:"workSeconds,omitempty"`
	RestSeconds *int     `json:"restSeconds,omitempty"`
	// Raw is required even when the structured fields are complete: whatever
	// they cannot express, the source string still can.
	Raw string `json:"raw" jsonschema:"the source string this was parsed from, always required"`
}

type Load struct {
	Mode  string   `json:"mode" jsonschema:"one of fixed range bodyweight percentage rpe_based"`
	Value *float64 `json:"value,omitempty"`
	Min   *float64 `json:"min,omitempty"`
	Max   *float64 `json:"max,omitempty"`
	Unit  string   `json:"unit,omitempty"`
	Raw   string   `json:"raw,omitempty"`
}

type Item struct {
	ItemKey      string    `json:"itemKey"`
	Kind         string    `json:"kind" jsonschema:"exercise reference benchmark or tracking"`
	Order        int       `json:"order"`
	Name         string    `json:"name"`
	ExerciseID   *string   `json:"exerciseId,omitempty"`
	Custom       bool      `json:"custom,omitempty"`
	Sets         *int      `json:"sets,omitempty"`
	Reps         *Quantity `json:"reps,omitempty"`
	Load         *Load     `json:"load,omitempty"`
	Tempo        string    `json:"tempo,omitempty"`
	RestSec      *int      `json:"restSec,omitempty"`
	TargetRPE    *float64  `json:"targetRpe,omitempty"`
	CoachingCues []string  `json:"coachingCues,omitempty"`
	Notes        string    `json:"notes,omitempty"`
	Checklist    []string  `json:"checklist,omitempty"`
}

type Section struct {
	Kind  string `json:"kind" jsonschema:"warmup main accessory conditioning skill or cooldown"`
	Order int    `json:"order"`
	Name  string `json:"name,omitempty"`
	Notes string `json:"notes,omitempty"`
	// Sections own their items: one ordering, not a section holding keys that
	// point at a separate flat list.
	Items []Item `json:"items"`
}

type ProgramContext struct {
	ProgramID  string `json:"programId"`
	WorkoutKey string `json:"workoutKey,omitempty"`
	Week       *int   `json:"week,omitempty"`
	PhaseKey   string `json:"phaseKey,omitempty"`
}

type Generation struct {
	GeneratorVersion string `json:"generatorVersion,omitempty"`
	Provider         string `json:"provider,omitempty"`
	Model            string `json:"model,omitempty"`
	PromptVersion    string `json:"promptVersion,omitempty"`
	ContextVersion   string `json:"contextVersion,omitempty"`
	ContextHash      string `json:"contextHash,omitempty"`
	OperationID      string `json:"operationId,omitempty"`
}

type CreateInput struct {
	LocalDate         string          `json:"localDate" jsonschema:"the athlete's own local date YYYY-MM-DD"`
	SessionOfDay      string          `json:"sessionOfDay" jsonschema:"morning or evening"`
	ScheduledTimezone string          `json:"scheduledTimezone" jsonschema:"IANA identifier such as America/Chicago"`
	ScheduledAt       string          `json:"scheduledAt,omitempty"`
	Name              string          `json:"name"`
	WorkoutType       string          `json:"workoutType,omitempty"`
	EstimatedDuration *int            `json:"estimatedDuration,omitempty"`
	Sections          []Section       `json:"sections"`
	ProgramContext    *ProgramContext `json:"programContext,omitempty" jsonschema:"omit for a freestanding session"`
	// Only a session carrying one advances program progress. Leave empty for
	// optional or recovery work.
	ProgressionRequirementID string      `json:"progressionRequirementId,omitempty"`
	Generation               *Generation `json:"generation,omitempty"`
	IdempotencyKey           string      `json:"idempotencyKey,omitempty" jsonschema:"supply one so a retry returns the original"`
}

type GetInput struct {
	PrescriptionID string `json:"prescriptionId"`
}

type SupersedeInput struct {
	SlotID string `json:"slotId"`
	// ExpectedRevision is required, not optional. Without it a caller working
	// from a stale read would silently overwrite a newer prescription, which is
	// exactly the lost update the slot's revision exists to make visible.
	ExpectedRevision int    `json:"expectedRevision" jsonschema:"the slot revision you just read"`
	Reason           string `json:"reason,omitempty" jsonschema:"why it is being replaced, recorded in history"`

	LocalDate                string          `json:"localDate"`
	SessionOfDay             string          `json:"sessionOfDay"`
	ScheduledTimezone        string          `json:"scheduledTimezone"`
	ScheduledAt              string          `json:"scheduledAt,omitempty"`
	Name                     string          `json:"name"`
	WorkoutType              string          `json:"workoutType,omitempty"`
	EstimatedDuration        *int            `json:"estimatedDuration,omitempty"`
	Sections                 []Section       `json:"sections"`
	ProgramContext           *ProgramContext `json:"programContext,omitempty"`
	ProgressionRequirementID string          `json:"progressionRequirementId,omitempty"`
	Generation               *Generation     `json:"generation,omitempty"`
}

// source is stamped here rather than accepted as input: an assistant does not
// get to claim a session came from a coach.
type source struct {
	Kind string `json:"kind"`
}

func aiGenerated() source { return source{Kind: "ai_generated"} }

/*
Explain turns a failure into something an assistant can act on.

Status codes mean specific things on this surface. An assistant told "409"
retries; one told what 409 means here re-reads the slot and supersedes, which is
the correct recovery.
*/
func explain(err error) *mcp.CallToolResult {
	var apiErr *client.APIError
	if !asAPIError(err, &apiErr) {
		return textResult(true, fmt.Sprintf("Request failed: %v", err))
	}

	guidance := map[int]string{
		401: "The service token is missing, expired or not valid for this API.",
		403: "The token lacks the scope this action requires, or the athlete has not granted it.",
		404: "Not found, or it belongs to a different athlete.",
		409: "This slot is already taken, or it changed since you read it. Use the slotId and revision " +
			"returned here as expectedRevision and call supersede_workout_prescription instead of creating.",
		422: "The request was well formed but failed validation. See errors for the specific fields.",
		429: "Rate limited. Wait before retrying.",
	}[apiErr.Status]

	/*
	 * Safety findings replace the generic guidance when present.
	 *
	 * "This session did not pass safety validation" tells an assistant nothing
	 * it can act on -- it cannot know which item was wrong or which rule fired,
	 * so its only move is to resubmit a guess. The findings name both. They were
	 * being dropped before reaching here, which made the refusal a dead end.
	 */
	if len(apiErr.Findings) > 0 {
		guidance = "This session was refused. Each finding names the item and the rule. " +
			"Fix those items and submit a corrected session -- do not resubmit this one unchanged."
	}

	body := map[string]any{
		"status":    apiErr.Status,
		"error":     apiErr.Error(),
		"errors":    apiErr.Errors,
		"guidance":  guidance,
		"requestId": apiErr.RequestID,
	}
	if len(apiErr.Findings) > 0 {
		body["findings"] = apiErr.Findings
	}
	if len(apiErr.Warnings) > 0 {
		body["warnings"] = apiErr.Warnings
	}
	// A 409 carries slotId and revision here, which is what supersede needs to
	// recover. Merged rather than nested so the assistant reads them alongside
	// the guidance that tells it to use them.
	for key, value := range apiErr.Details {
		if _, taken := body[key]; !taken {
			body[key] = value
		}
	}

	payload, _ := json.MarshalIndent(body, "", "  ")

	return textResult(true, string(payload))
}

func asAPIError(err error, target **client.APIError) bool {
	if e, ok := err.(*client.APIError); ok {
		*target = e
		return true
	}
	return false
}

func textResult(isError bool, text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: isError,
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}
}

func rawResult(raw json.RawMessage) *mcp.CallToolResult {
	var pretty any
	if err := json.Unmarshal(raw, &pretty); err == nil {
		if formatted, err := json.MarshalIndent(pretty, "", "  "); err == nil {
			return textResult(false, string(formatted))
		}
	}
	return textResult(false, string(raw))
}

// Register wires the four tools onto a server.
func Register(server *mcp.Server, api *client.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_generation_context",
		InputSchema: portableSchema[ContextInput](),
		Description: "Everything needed to design a session for the athlete this token acts for: " +
			"profile, program position, equipment, injuries, limitations, readiness, recent " +
			"prescriptions and executions. Every optional block carries a state — \"not_reported\" " +
			"means nobody asked, which is NOT the same as \"confirmed_none\". Never treat an empty " +
			"injuries list as an absence of injuries unless the state says confirmed_none. " +
			"When sessionOfDay is evening the response carries an eveningAdjustment directive; " +
			"follow it rather than deriving your own adjustment from the morning's RPEs.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ContextInput) (*mcp.CallToolResult, any, error) {
		query := url.Values{}
		if in.WorkoutLimit > 0 {
			query.Set("workoutLimit", strconv.Itoa(in.WorkoutLimit))
		}
		if in.PerformanceDays > 0 {
			query.Set("performanceDays", strconv.Itoa(in.PerformanceDays))
		}
		if in.ReadinessDays > 0 {
			query.Set("readinessDays", strconv.Itoa(in.ReadinessDays))
		}
		if len(in.Exercises) > 0 {
			query.Set("exercises", strings.Join(in.Exercises, ","))
		}
		if in.SessionOfDay != "" {
			query.Set("sessionOfDay", in.SessionOfDay)
		}

		raw, _, err := api.TrainingContext(ctx, query)
		if err != nil {
			return explain(err), nil, nil
		}
		return rawResult(raw), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_workout_prescription",
		InputSchema: portableSchema[CreateInput](),
		Description: "Create one prescribed session for a morning or evening slot. Fails with 409 " +
			"if that slot already has a prescription — use supersede_workout_prescription to " +
			"replace one. Supply an idempotencyKey so a retry returns the original rather than " +
			"creating a second. Every reps value must include `raw`, the source string, alongside " +
			"its structured fields. You cannot create a session that is already completed; " +
			"prescriptions start as scheduled.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in CreateInput) (*mcp.CallToolResult, any, error) {
		body := struct {
			CreateInput
			Source source `json:"source"`
		}{CreateInput: in, Source: aiGenerated()}

		raw, _, err := api.CreatePrescription(ctx, body)
		if err != nil {
			return explain(err), nil, nil
		}
		return rawResult(raw), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_workout_prescription",
		InputSchema: portableSchema[GetInput](),
		Description: "Read back a prescription by id, to confirm what was stored. Returns 404 if " +
			"it belongs to a different athlete.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetInput) (*mcp.CallToolResult, any, error) {
		raw, _, err := api.GetPrescription(ctx, in.PrescriptionID)
		if err != nil {
			return explain(err), nil, nil
		}
		return rawResult(raw), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "supersede_workout_prescription",
		InputSchema: portableSchema[SupersedeInput](),
		Description: "Replace an unstarted session — for example after poor readiness or a hard " +
			"previous session. Requires expectedRevision from the slot you just read; a mismatch " +
			"returns 409, meaning someone changed it and you should re-read rather than overwrite. " +
			"Refuses once the athlete has started the session. The replaced prescription is kept, " +
			"not deleted.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SupersedeInput) (*mcp.CallToolResult, any, error) {
		/*
		 * slotId identifies the slot in the PATH, and the request body is
		 * validated strictly, so sending it in the body too is rejected outright
		 * with "Unrecognized key: slotId" -- this tool could not succeed at all.
		 *
		 * Shadowed rather than removed from SupersedeInput, because the field is
		 * how the tool takes the slot from its caller. An outer field of the same
		 * name at depth 0 wins over the embedded one at depth 1, and empty plus
		 * omitempty drops it from the encoded body. supersedeBodyOmitsSlotID in
		 * the tests pins this, since the mechanism is not obvious on sight.
		 */
		body := struct {
			SupersedeInput
			SlotID string `json:"slotId,omitempty"`
			Source source `json:"source"`
		}{SupersedeInput: in, Source: aiGenerated()}

		raw, _, err := api.Supersede(ctx, in.SlotID, body)
		if err != nil {
			return explain(err), nil, nil
		}
		return rawResult(raw), nil, nil
	})
}
