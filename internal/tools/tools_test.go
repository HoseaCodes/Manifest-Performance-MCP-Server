package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hoseacodes/manifestfitness/workout-mcp/internal/client"
)

/*
What these assert is mostly absence: the tools an assistant can reach, and the
fields those tools can send. A capability that does not exist cannot be misused,
and these are what stop one being added by accident later.
*/

func TestNoInputNamesAnAthlete(t *testing.T) {
	// The athlete comes from the token's `sub`. A field here would be a way to
	// act for someone else, so there must not be one.
	for _, input := range []any{ContextInput{}, CreateInput{}, GetInput{}, SupersedeInput{}} {
		typ := reflect.TypeOf(input)
		for i := 0; i < typ.NumField(); i++ {
			name := strings.ToLower(typ.Field(i).Name)
			for _, forbidden := range []string{"athlete", "userid", "sub", "owner"} {
				if strings.Contains(name, forbidden) {
					t.Errorf("%s has field %q — inputs must not name an athlete", typ.Name(), typ.Field(i).Name)
				}
			}
		}
	}
}

func TestCreateCannotSetStatus(t *testing.T) {
	// Enforcement by absence: a completed session is not expressible, rather
	// than forbidden by a check someone could remove.
	typ := reflect.TypeOf(CreateInput{})
	for i := 0; i < typ.NumField(); i++ {
		if strings.EqualFold(typ.Field(i).Name, "Status") {
			t.Error("CreateInput has a Status field — a service token must not create a completed session")
		}
	}
}

func TestCreateCannotSetSource(t *testing.T) {
	// An assistant does not get to claim a session came from a coach.
	typ := reflect.TypeOf(CreateInput{})
	for i := 0; i < typ.NumField(); i++ {
		if strings.EqualFold(typ.Field(i).Name, "Source") {
			t.Error("CreateInput has a Source field — source is stamped server-side")
		}
	}
}

func TestSupersedeRequiresExpectedRevision(t *testing.T) {
	// A pointer or omitempty would make the compare-and-swap guard optional,
	// and a stale caller would overwrite silently.
	field, ok := reflect.TypeOf(SupersedeInput{}).FieldByName("ExpectedRevision")
	if !ok {
		t.Fatal("SupersedeInput has no ExpectedRevision")
	}
	if strings.Contains(field.Tag.Get("json"), "omitempty") {
		t.Error("ExpectedRevision is omitempty — the guard must always be sent")
	}
	if field.Type.Kind() == reflect.Ptr {
		t.Error("ExpectedRevision is a pointer — it must not be omissible")
	}
}

func TestQuantityAlwaysCarriesRaw(t *testing.T) {
	// Whatever the structured fields cannot express, the source string still
	// can. Losing it is how "30 sec" became the number 30.
	field, _ := reflect.TypeOf(Quantity{}).FieldByName("Raw")
	if strings.Contains(field.Tag.Get("json"), "omitempty") {
		t.Error("Quantity.Raw is omitempty — the source string must always be sent")
	}
}

func TestRegistersExactlyFourTools(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	api, _ := client.New("https://api.test", "token")
	Register(server, api)

	// Registering more would widen the surface an assistant can reach; this is
	// the list the decision log approved.
	want := map[string]bool{
		"get_generation_context":         true,
		"create_workout_prescription":    true,
		"get_workout_prescription":       true,
		"supersede_workout_prescription": true,
	}

	got := registeredToolNames(t, server)
	if len(got) != len(want) {
		t.Fatalf("registered %d tools: %v", len(got), got)
	}
	for _, name := range got {
		if !want[name] {
			t.Errorf("unexpected tool %q", name)
		}
	}
}

func TestNoToolRecordsCompletionReadinessOrDeletion(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	api, _ := client.New("https://api.test", "token")
	Register(server, api)

	for _, name := range registeredToolNames(t, server) {
		for _, forbidden := range []string{"complete", "readiness", "delete", "record"} {
			if strings.Contains(name, forbidden) {
				t.Errorf("tool %q exposes a capability MCP must not have", name)
			}
		}
	}
}

func TestStampsSourceAsAIGenerated(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"created":true}`))
	}))
	defer server.Close()

	api, _ := client.New(server.URL, "token")
	if _, _, err := api.CreatePrescription(context.Background(), struct {
		CreateInput
		Source source `json:"source"`
	}{CreateInput{Name: "Session"}, aiGenerated()}); err != nil {
		t.Fatalf("CreatePrescription: %v", err)
	}

	src, _ := body["source"].(map[string]any)
	if src["kind"] != "ai_generated" {
		t.Errorf("source.kind = %v", src["kind"])
	}
}

func TestExplainGivesActionableGuidance(t *testing.T) {
	cases := []struct {
		status int
		expect string
	}{
		// An assistant told "409" retries. One told what it means re-reads and
		// supersedes, which is the correct recovery.
		{409, "supersede"},
		{403, "scope"},
		{404, "different athlete"},
		{429, "Rate limited"},
	}

	for _, tc := range cases {
		result := explain(&client.APIError{Status: tc.status, Message: "x", RequestID: "req-1"})
		if !result.IsError {
			t.Errorf("status %d: expected an error result", tc.status)
		}

		text := result.Content[0].(*mcp.TextContent).Text
		if !strings.Contains(strings.ToLower(text), strings.ToLower(tc.expect)) {
			t.Errorf("status %d: guidance %q does not mention %q", tc.status, text, tc.expect)
		}
		if !strings.Contains(text, "req-1") {
			t.Errorf("status %d: request id was not carried through", tc.status)
		}
	}
}

func TestExplainHandlesNonAPIErrors(t *testing.T) {
	result := explain(context.DeadlineExceeded)
	if !result.IsError {
		t.Error("expected an error result")
	}
	if result.Content[0].(*mcp.TextContent).Text == "" {
		t.Error("expected a message")
	}
}

// registeredToolNames lists what a client would actually see.
//
// Goes through the real protocol over an in-memory transport rather than
// reading the server's internals: what matters is the surface an assistant is
// offered, which is the result of a tools/list call.
func registeredToolNames(t *testing.T, server *mcp.Server) []string {
	t.Helper()

	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer serverSession.Close()

	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).
		Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer clientSession.Close()

	result, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}

	names := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
	}
	return names
}
