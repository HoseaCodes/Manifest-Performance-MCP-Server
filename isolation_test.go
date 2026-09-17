package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

/*
Phase 5's exit criterion, asserted mechanically.

The MCP server must provably hold no database access. In Go this is a stronger
claim than it was in JavaScript: the module graph is explicit and complete, so
"nothing in this binary can reach MongoDB" is checkable rather than a statement
about what a bundler happened to include.
*/

// Drivers and clients that would give this binary direct data access.
var forbiddenModules = []string{
	"go.mongodb.org/mongo-driver",
	"github.com/lib/pq",
	"github.com/go-sql-driver/mysql",
	"github.com/jackc/pgx",
	"github.com/redis/go-redis",
	"gorm.io/gorm",
}

func TestBinaryCannotReachADatabase(t *testing.T) {
	// `go list -deps` walks the full transitive graph of what actually compiles
	// into the binary, not just direct requirements — so a driver pulled in by
	// a dependency would still be caught.
	out, err := exec.Command("go", "list", "-deps", "./...").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}

	deps := string(out)
	for _, module := range forbiddenModules {
		if strings.Contains(deps, module) {
			t.Errorf("dependency graph includes %q — this binary can reach a database", module)
		}
	}

	// `database/sql` would mean a driver is expected somewhere.
	for _, line := range strings.Split(deps, "\n") {
		if strings.TrimSpace(line) == "database/sql" {
			t.Error("dependency graph includes database/sql")
		}
	}
}

func TestSourceReferencesNoConnectionString(t *testing.T) {
	forbidden := []string{"MONGODB_URI", "MONGODB_URL", "DATABASE_URL"}

	forEachGoFile(t, func(path, source string) {
		for _, name := range forbidden {
			if strings.Contains(stripComments(source), name) {
				t.Errorf("%s references %q", path, name)
			}
		}
	})
}

func TestCallsNoAthleteAuthenticatedRoute(t *testing.T) {
	/*
	   The route prefix is the control, not vocabulary.

	   An earlier version of this check also failed on any mention of
	   "readiness", which was wrong: reading readiness as part of the generation
	   context is exactly what this server should do. It is *writing* it that
	   belongs to the athlete alone — and that is a route, not a word. A check
	   that bans the topic rather than the capability trains people to work
	   around it.
	*/
	forEachGoFile(t, func(path, source string) {
		if strings.Contains(stripComments(source), `"/api/user/`) {
			t.Errorf("%s calls an athlete-authenticated route", path)
		}
	})
}

func TestOnlyInternalEndpointsAreReachable(t *testing.T) {
	// Every route this binary can call, so widening the surface is a visible
	// change to this list rather than a quiet addition.
	allowed := map[string]bool{
		"/api/internal/me/training-context":       true,
		"/api/internal/me/workout-prescriptions":  true,
		"/api/internal/me/workout-prescriptions/": true,
		"/api/internal/me/workout-slots/":         true,
	}

	source, err := os.ReadFile(filepath.Join("internal", "client", "client.go"))
	if err != nil {
		t.Fatalf("read client.go: %v", err)
	}

	for _, line := range strings.Split(stripComments(string(source)), "\n") {
		if !strings.Contains(line, "/api/") {
			continue
		}
		matched := false
		for prefix := range allowed {
			if strings.Contains(line, prefix) {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("client reaches an unlisted route: %s", strings.TrimSpace(line))
		}
	}
}

func forEachGoFile(t *testing.T, check func(path, source string)) {
	t.Helper()

	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		// This file names the forbidden strings in order to forbid them.
		if strings.HasSuffix(path, "isolation_test.go") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		check(path, string(source))
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// stripComments removes comment lines so documentation explaining what is
// absent does not itself trip the check.
func stripComments(source string) string {
	var kept []string
	inBlock := false

	for _, line := range strings.Split(source, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case inBlock:
			if strings.Contains(trimmed, "*/") {
				inBlock = false
			}
		case strings.HasPrefix(trimmed, "/*"):
			if !strings.Contains(trimmed, "*/") {
				inBlock = true
			}
		case strings.HasPrefix(trimmed, "//"):
			// skip
		default:
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}
