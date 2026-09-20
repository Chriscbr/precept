package cmd

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Chriscbr/precept/internal/discover"
)

func TestListClaimIDUniqueness(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	if output, err := exec.Command("git", "init", "-q", repository).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	const source = `package example

// INVARIANT shared: first claim
func First() {}

// PRECONDITION: default ID
func NewBuffer() {}
`
	firstPath := filepath.Join(repository, "a.go")
	mustWriteFile(t, firstPath, source, 0o644)
	otherDirectory := filepath.Join(repository, "other")
	if err := os.MkdirAll(otherDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(otherDirectory, "b.go"), source, 0o644)
	const duplicateClaims = `
// ASSERTION shared: an explicit duplicate across claim types
func Second() {}

// INVARIANT newbuffer-precondition-1: explicit ID collides with the generated ID
func Third() {}
`
	wantErrors := "error: claim ID \"shared\" is used twice in the same file (a.go)\n" +
		"error: claim ID \"newbuffer-precondition-1\" is used twice in the same file (a.go)\n"
	for _, format := range []string{"text", "compact", "json"} {
		for _, duplicates := range []bool{true, false} {
			name := format + "/reused-across-files"
			if duplicates {
				name = format + "/duplicates-within-file"
			}
			t.Run(name, func(t *testing.T) {
				contents := source
				if duplicates {
					contents += duplicateClaims
				}
				mustWriteFile(t, firstPath, contents, 0o644)
				args := []string{"list", repository}
				if format != "text" {
					args = append(args, "--"+format)
				}
				stdout, stderr, code, _ := executeCLI(t, args)
				if duplicates {
					if code != 2 || stdout != "" || !strings.HasSuffix(stderr, wantErrors) {
						t.Fatalf("exit = %d, stdout = %q, stderr = %s; want exit 2, empty stdout and errors %q", code, stdout, stderr, wantErrors)
					}
					if strings.Count(stderr, "error:") != 2 {
						t.Fatalf("expected one error per duplicated ID: %s", stderr)
					}
					return
				}
				if code != 0 || strings.Contains(stderr, "error:") || strings.Contains(stderr, "warning") {
					t.Fatalf("cross-file IDs failed: exit = %d, stderr = %s", code, stderr)
				}
				if format == "json" {
					var document listDocument
					if err := json.Unmarshal([]byte(stdout), &document); err != nil {
						t.Fatalf("decode JSON stdout: %v", err)
					}
					if len(document.Claims) != 4 {
						t.Fatalf("listed %d claims, want 4", len(document.Claims))
					}
				}
			})
		}
	}
}

func TestVerifyClaimSelection(t *testing.T) {
	base := t.TempDir()
	repository := filepath.Join(base, "repository")
	if output, err := exec.Command("git", "init", "-q", repository).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	sourcePath := filepath.Join(repository, "buffer.go")
	mustWriteFile(t, sourcePath, `package queue

// INVARIANT buffer-single-owner: exactly one owner
type Buffer struct{}

// PRECONDITION: capacity is positive
func NewBuffer() *Buffer { return &Buffer{} }

// POSTCONDITION: buffer is closed
// POSTCONDITION: resources are released
func (b *Buffer) Close() {}
`, 0o644)
	otherDirectory := filepath.Join(repository, "other")
	binDirectory := filepath.Join(base, "bin")
	for _, directory := range []string{otherDirectory, binDirectory} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Exercise collisions between explicit IDs, generated IDs, and both kinds.
	mustWriteFile(t, filepath.Join(otherDirectory, "other.go"), `package other

// INVARIANT buffer-single-owner: another owner
type Buffer struct{}

// PRECONDITION: another capacity
func NewBuffer() {}

// INVARIANT buffer-close-postcondition-2: explicit ID collides with a default
func Other() {}
`, 0o644)
	emptyPath := filepath.Join(repository, "empty.go")
	mustWriteFile(t, emptyPath, "package queue\n", 0o644)
	callLog := filepath.Join(base, "calls.log")
	mustWriteFile(t, filepath.Join(binDirectory, "codex"), `#!/bin/sh
if [ "$1" = "--version" ]; then
  printf '%s\n' 'codex-cli fake-1.0'
  exit 0
fi
sed -n '/^<claim>$/,/^<\/claim>$/p' >> "$PRECEPT_FAKE_CALL_LOG"
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"{\"verdict\":\"holds\",\"summary\":\"verified\",\"evidence\":[{\"file\":\"buffer.go\",\"start_line\":3,\"end_line\":3,\"reason\":\"fixture\"}],\"counterexample\":\"\"}"}}'
`, 0o755)
	t.Setenv("PRECEPT_FAKE_CALL_LOG", callLog)
	t.Setenv("PATH", binDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))

	stdout, _, err := executeCommand(t, []string{"list", "--json", sourcePath})
	if err != nil {
		t.Fatal(err)
	}
	var listed listDocument
	if err := json.Unmarshal([]byte(stdout), &listed); err != nil {
		t.Fatal(err)
	}
	allIDs := []string{"buffer-single-owner", "newbuffer-precondition-1", "buffer-close-postcondition-1", "buffer-close-postcondition-2"}
	var listedIDs []string
	for _, claim := range listed.Claims {
		listedIDs = append(listedIDs, claim.ID)
	}
	if !reflect.DeepEqual(listedIDs, allIDs) {
		t.Fatalf("list IDs = %v, want %v", listedIDs, allIDs)
	}

	for _, test := range []struct {
		name    string
		scope   string
		ids     []string
		wantIDs []string
		error   string
		invalid bool
	}{
		{name: "all", wantIDs: allIDs},
		{name: "explicit", ids: []string{allIDs[0]}, wantIDs: allIDs[:1]},
		{name: "generated", ids: []string{allIDs[3]}, wantIDs: allIDs[3:]},
		{name: "repeated and out of order", ids: []string{allIDs[3], allIDs[1], allIDs[3]}, wantIDs: []string{allIDs[1], allIDs[3]}},
		{name: "unknown", ids: []string{"missing"}, error: `claim ID "missing" was not found`},
		{name: "case sensitive", ids: []string{"Buffer-single-owner"}, error: "was not found"},
		{name: "unknown after valid", ids: []string{allIDs[0], "missing"}, error: "was not found"},
		{name: "empty scope", scope: emptyPath, ids: []string{allIDs[0]}, error: "was not found"},
		{name: "all across files", scope: repository, wantIDs: append(append([]string{}, allIDs...), allIDs[0], allIDs[1], allIDs[3])},
		{name: "reused explicit", scope: repository, ids: []string{allIDs[0]}, wantIDs: []string{allIDs[0], allIDs[0]}},
		{name: "reused generated", scope: repository, ids: []string{allIDs[1]}, wantIDs: []string{allIDs[1], allIDs[1]}},
		{name: "explicit matches generated in another file", scope: repository, ids: []string{allIDs[3]}, wantIDs: []string{allIDs[3], allIDs[3]}},
		{name: "repeated flags across files", scope: repository, ids: []string{allIDs[1], allIDs[0], allIDs[1]}, wantIDs: []string{allIDs[0], allIDs[1], allIDs[0], allIDs[1]}},
		{name: "duplicate IDs fail before verification", scope: repository, invalid: true, error: `error: claim ID "repeated" is used twice in the same file (invalid.go)`},
		{name: "duplicates outside selected claims still fail", scope: repository, ids: []string{allIDs[1]}, invalid: true, error: `error: claim ID "repeated" is used twice in the same file (invalid.go)`},
		{name: "duplicates outside scope are allowed", ids: []string{allIDs[1]}, invalid: true, wantIDs: []string{allIDs[1]}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.invalid {
				invalidPath := filepath.Join(repository, "invalid.go")
				mustWriteFile(t, invalidPath, "package queue\n// INVARIANT repeated: first\n// ASSERTION repeated: second\nfunc Invalid() {}\n", 0o644)
				t.Cleanup(func() { _ = os.Remove(invalidPath) })
			}
			mustWriteFile(t, callLog, "", 0o600)
			args := []string{"verify", "--harness", "codex", "--jobs", "1", "--json"}
			for _, id := range test.ids {
				args = append(args, "--claim", id)
			}
			scope := test.scope
			if scope == "" {
				scope = sourcePath
			}
			args = append(args, scope)
			stdout, stderr, code, logPath := executeCLI(t, args)
			t.Cleanup(func() { _ = os.Remove(logPath) })
			calls, err := os.ReadFile(callLog)
			if err != nil {
				t.Fatal(err)
			}
			if test.error != "" {
				if code != 2 || stdout != "" || !strings.Contains(stderr, test.error) || len(calls) != 0 {
					t.Fatalf("exit = %d, stderr = %s, agent calls = %s; want %q and no agent calls", code, stderr, calls, test.error)
				}
				return
			}
			if code != 0 {
				t.Fatalf("exit = %d, stderr = %s", code, stderr)
			}
			var document struct {
				Outcomes []struct{ Claim discover.Claim } `json:"outcomes"`
				Summary  struct{ Total int }              `json:"summary"`
			}
			if err := json.Unmarshal([]byte(stdout), &document); err != nil {
				t.Fatal(err)
			}
			var gotIDs []string
			var wantCalls strings.Builder
			for _, outcome := range document.Outcomes {
				gotIDs = append(gotIDs, outcome.Claim.ID)
				wantCalls.WriteString("<claim>\n" + outcome.Claim.Text + "\n</claim>\n")
			}
			if !reflect.DeepEqual(gotIDs, test.wantIDs) || document.Summary.Total != len(test.wantIDs) {
				t.Fatalf("verified IDs = %v, total = %d; want %v", gotIDs, document.Summary.Total, test.wantIDs)
			}
			if string(calls) != wantCalls.String() {
				t.Fatalf("agent received claims %s, want %s", calls, wantCalls.String())
			}
			log, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range test.ids {
				if !strings.Contains(string(log), "selected_claim: "+id+"\n") || !strings.Contains(string(log), "claim_id: "+id+"\n") {
					t.Errorf("log is missing selected claim %q", id)
				}
				if !strings.Contains(stderr, "("+id+")") {
					t.Errorf("progress is missing claim ID %q: %s", id, stderr)
				}
			}
		})
	}
}
