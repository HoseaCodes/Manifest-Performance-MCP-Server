package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// A stub server rather than a mocked transport, so what is checked is the
// request actually formed — headers, paths, bodies.
func stub(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(server.Close)

	api, err := New(server.URL, "test-token")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return api, server
}

func TestNewRefusesMissingConfiguration(t *testing.T) {
	// A misconfigured server must refuse to start rather than appear healthy
	// and fail on the athlete's first request.
	if _, err := New("", "token"); err == nil {
		t.Error("expected an error when the base URL is missing")
	}
	if _, err := New("https://api.test", ""); err == nil {
		t.Error("expected an error when the token is missing")
	}
	if _, err := New("https://api.test", "   "); err == nil {
		t.Error("expected whitespace to count as missing")
	}
}

func TestSendsBearerTokenAndAddressesMe(t *testing.T) {
	var gotAuth, gotPath string
	api, _ := stub(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("x-request-id", "req-1")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	if _, _, err := api.TrainingContext(context.Background(), nil); err != nil {
		t.Fatalf("TrainingContext: %v", err)
	}

	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	// The athlete comes from the token, so the path names no one.
	if gotPath != "/api/internal/me/training-context" {
		t.Errorf("path = %q", gotPath)
	}
}

func TestForwardsQueryParameters(t *testing.T) {
	var got url.Values
	api, _ := stub(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		_, _ = w.Write([]byte(`{}`))
	})

	query := url.Values{}
	query.Set("workoutLimit", "10")
	query.Set("exercises", "Back Squat,Bench Press")

	if _, _, err := api.TrainingContext(context.Background(), query); err != nil {
		t.Fatalf("TrainingContext: %v", err)
	}
	if got.Get("exercises") != "Back Squat,Bench Press" {
		t.Errorf("exercises = %q", got.Get("exercises"))
	}
}

func TestEscapesPathParameters(t *testing.T) {
	var gotPath string
	api, _ := stub(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		_, _ = w.Write([]byte(`{}`))
	})

	// An id that is not an id must not be able to reach another route.
	if _, _, err := api.GetPrescription(context.Background(), "../../admin"); err != nil {
		t.Fatalf("GetPrescription: %v", err)
	}
	if gotPath != "/api/internal/me/workout-prescriptions/..%2F..%2Fadmin" {
		t.Errorf("path = %q — traversal was not escaped", gotPath)
	}
}

func TestSupersedeAddressesTheSlot(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	api, _ := stub(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{}`))
	})

	body := map[string]any{"expectedRevision": 3}
	if _, _, err := api.Supersede(context.Background(), "slot-1", body); err != nil {
		t.Fatalf("Supersede: %v", err)
	}

	// Addressed to the slot, not the prescription: prescriptions are immutable
	// and the slot owns the pointer.
	if gotPath != "/api/internal/me/workout-slots/slot-1/supersede" {
		t.Errorf("path = %q", gotPath)
	}
	// The compare-and-swap guard must reach the server, not be lost in
	// translation.
	if gotBody["expectedRevision"] != float64(3) {
		t.Errorf("expectedRevision = %v", gotBody["expectedRevision"])
	}
}

func TestSurfacesAPIErrorsWithDetail(t *testing.T) {
	api, _ := stub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-request-id", "req-42")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":"failed validation","errors":[{"path":"sections.0.items.0.reps.raw","message":"Required"}]}`))
	})

	_, _, err := api.CreatePrescription(context.Background(), map[string]any{})
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T", err)
	}

	if apiErr.Status != 422 {
		t.Errorf("status = %d", apiErr.Status)
	}
	// Without the per-field detail the next attempt is a guess.
	if len(apiErr.Errors) != 1 || apiErr.Errors[0].Path != "sections.0.items.0.reps.raw" {
		t.Errorf("errors = %+v", apiErr.Errors)
	}
	// "It didn't work" is unresolvable without this.
	if apiErr.RequestID != "req-42" {
		t.Errorf("requestId = %q", apiErr.RequestID)
	}
}

func TestReportsStatusWhenBodyIsNotJSON(t *testing.T) {
	api, _ := stub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream exploded"))
	})

	_, _, err := api.GetPrescription(context.Background(), "p1")
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.Status != 502 {
		t.Errorf("status = %d", apiErr.Status)
	}
	if apiErr.Error() == "" {
		t.Error("expected a usable message even with an unparseable body")
	}
}
