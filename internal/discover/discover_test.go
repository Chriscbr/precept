package discover

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestResultJSONUsesSnakeCaseFields(t *testing.T) {
	t.Parallel()

	result := Result{
		Claims: []Claim{{
			ID:         "example-invariant-1",
			Marker:     MarkerInvariant,
			Text:       "claim",
			Package:    "sample",
			Kind:       "func",
			Symbol:     "Example",
			File:       "sample.go",
			MarkerLine: 1,
			StartLine:  2,
			EndLine:    3,
		}},
		Diagnostics: []Diagnostic{{File: "sample.go", Line: 4, Message: "message"}},
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	const want = `{"claims":[{"id":"example-invariant-1","marker":"INVARIANT","text":"claim","package":"sample","kind":"func","symbol":"Example","file":"sample.go","marker_line":1,"start_line":2,"end_line":3}],"diagnostics":[{"file":"sample.go","line":4,"message":"message"}]}`
	if string(encoded) != want {
		t.Fatalf("json.Marshal() = %s, want %s", encoded, want)
	}
}

func TestMarkerText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		raw        string
		wantMarker Marker
		wantID     string
		wantText   string
		wantOK     bool
	}{
		{name: "canonical invariant", raw: "// INVARIANT: result is non-negative", wantMarker: MarkerInvariant, wantText: "result is non-negative", wantOK: true},
		{name: "invariant without space after colon", raw: "// INVARIANT:result is non-negative", wantMarker: MarkerInvariant, wantText: "result is non-negative", wantOK: true},
		{name: "empty invariant", raw: "// INVARIANT:\t", wantMarker: MarkerInvariant, wantOK: true},
		{name: "canonical precondition", raw: "// PRECONDITION: input is valid", wantMarker: MarkerPrecondition, wantText: "input is valid", wantOK: true},
		{name: "precondition without space after colon", raw: "// PRECONDITION:input is valid", wantMarker: MarkerPrecondition, wantText: "input is valid", wantOK: true},
		{name: "empty precondition", raw: "// PRECONDITION:\t", wantMarker: MarkerPrecondition, wantOK: true},
		{name: "canonical postcondition", raw: "// POSTCONDITION: result is valid", wantMarker: MarkerPostcondition, wantText: "result is valid", wantOK: true},
		{name: "postcondition without space after colon", raw: "// POSTCONDITION:result is valid", wantMarker: MarkerPostcondition, wantText: "result is valid", wantOK: true},
		{name: "empty postcondition", raw: "// POSTCONDITION:\t", wantMarker: MarkerPostcondition, wantOK: true},
		{name: "canonical assertion", raw: "// ASSERTION: value is initialized", wantMarker: MarkerAssertion, wantText: "value is initialized", wantOK: true},
		{name: "assertion without space after colon", raw: "// ASSERTION:value is initialized", wantMarker: MarkerAssertion, wantText: "value is initialized", wantOK: true},
		{name: "empty assertion", raw: "// ASSERTION:\t", wantMarker: MarkerAssertion, wantOK: true},
		{name: "named invariant", raw: "// INVARIANT buffer-single-owner: one owner", wantMarker: MarkerInvariant, wantID: "buffer-single-owner", wantText: "one owner", wantOK: true},
		{name: "named precondition", raw: "// PRECONDITION input.valid_v2:valid input", wantMarker: MarkerPrecondition, wantID: "input.valid_v2", wantText: "valid input", wantOK: true},
		{name: "named postcondition", raw: "// POSTCONDITION Closed: closed: yes", wantMarker: MarkerPostcondition, wantID: "Closed", wantText: "closed: yes", wantOK: true},
		{name: "named assertion", raw: "// ASSERTION 123: initialized", wantMarker: MarkerAssertion, wantID: "123", wantText: "initialized", wantOK: true},
		{name: "named empty claim", raw: "// INVARIANT owner:\t", wantMarker: MarkerInvariant, wantID: "owner", wantOK: true},
		{name: "unicode ID", raw: "// INVARIANT 所有者: one owner", wantMarker: MarkerInvariant, wantID: "所有者", wantText: "one owner", wantOK: true},
		{name: "named lowercase marker", raw: "// invariant owner: ignored"},
		{name: "named missing separator", raw: "//INVARIANT owner: ignored"},
		{name: "named extra separator", raw: "//  INVARIANT owner: ignored"},
		{name: "extra space before ID", raw: "// INVARIANT  owner: ignored"},
		{name: "tab before ID", raw: "// INVARIANT\towner: ignored"},
		{name: "space inside ID", raw: "// INVARIANT one owner: ignored"},
		{name: "space after ID", raw: "// INVARIANT owner : ignored"},
		{name: "invalid ID punctuation", raw: "// INVARIANT owner!: ignored"},
		{name: "invalid ID prefix", raw: "// INVARIANT -owner: ignored"},
		{name: "named missing colon", raw: "// INVARIANT owner ignored"},
		{name: "removed precept", raw: "// PRECEPT: ignored"},
		{name: "invariant without separator space", raw: "//INVARIANT: ignored"},
		{name: "precondition without separator space", raw: "//PRECONDITION: ignored"},
		{name: "postcondition without separator space", raw: "//POSTCONDITION: ignored"},
		{name: "assertion without separator space", raw: "//ASSERTION: ignored"},
		{name: "two separator spaces", raw: "//  PRECONDITION: ignored"},
		{name: "tab separator", raw: "//\tPOSTCONDITION: ignored"},
		{name: "lowercase invariant", raw: "// invariant: ignored"},
		{name: "mixed-case precondition", raw: "// PreCondition: ignored"},
		{name: "lowercase postcondition", raw: "// postcondition: ignored"},
		{name: "lowercase assertion", raw: "// assertion: ignored"},
		{name: "space before colon", raw: "// INVARIANT : ignored"},
		{name: "missing colon", raw: "// PRECONDITION ignored"},
		{name: "longer identifier", raw: "// POSTCONDITIONALLY: ignored"},
		{name: "block comment", raw: "/* INVARIANT: ignored */"},
		{name: "embedded", raw: "// note: PRECONDITION: ignored"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			gotMarker, gotID, gotText, gotOK := markerText(test.raw)
			if gotID != test.wantID {
				t.Fatalf("markerText(%q) ID = %q, want %q", test.raw, gotID, test.wantID)
			}
			if gotOK != test.wantOK || gotMarker != test.wantMarker || gotText != test.wantText {
				t.Fatalf(
					"markerText(%q) = (%q, %q, %t), want (%q, %q, %t)",
					test.raw,
					gotMarker,
					gotText,
					gotOK,
					test.wantMarker,
					test.wantText,
					test.wantOK,
				)
			}
		})
	}
}

func TestScanAssociatesClaimsWithSubjects(t *testing.T) {
	t.Parallel()

	repoRoot := absoluteFixturePath(t, "testdata")
	result, err := Scan(repoRoot, filepath.Join(repoRoot, "valid"))
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	wantClaims := []Claim{
		{ID: "fixture-invariant-1", Marker: MarkerInvariant, Text: "package documentation is associated with the package", Package: "fixture", Kind: "package", Symbol: "fixture", File: "valid/a_claims.go", MarkerLine: 1, StartLine: 2, EndLine: 2},
		{ID: "documented-precondition-1", Marker: MarkerPrecondition, Text: "function documentation is associated with the function", Package: "fixture", Kind: "func", Symbol: "Documented", File: "valid/a_claims.go", MarkerLine: 4, StartLine: 5, EndLine: 5},
		{ID: "inbody-invariant-1", Marker: MarkerInvariant, Text: "claims in a function body are associated with the function", Package: "fixture", Kind: "func", Symbol: "InBody", File: "valid/a_claims.go", MarkerLine: 8, StartLine: 7, EndLine: 10},
		{ID: "cache-get-postcondition-1", Marker: MarkerPostcondition, Text: "claims in a method body are associated with the method", Package: "fixture", Kind: "method", Symbol: "(*Cache).Get", File: "valid/a_claims.go", MarkerLine: 15, StartLine: 14, EndLine: 17},
		{ID: "named-invariant-1", Marker: MarkerInvariant, Text: "named types remain supported", Package: "fixture", Kind: "type", Symbol: "Named", File: "valid/a_claims.go", MarkerLine: 19, StartLine: 20, EndLine: 25},
		{ID: "named-value-invariant-1", Marker: MarkerInvariant, Text: "field documentation is associated with the field", Package: "fixture", Kind: "field", Symbol: "Named.Value", File: "valid/a_claims.go", MarkerLine: 21, StartLine: 22, EndLine: 22},
		{ID: "named-count-invariant-1", Marker: MarkerInvariant, Text: "trailing field comments are associated with the field", Package: "fixture", Kind: "field", Symbol: "Named.Count", File: "valid/a_claims.go", MarkerLine: 24, StartLine: 24, EndLine: 24},
		{ID: "grouped-precondition-1", Marker: MarkerPrecondition, Text: "grouped type specifications are associated with the named type", Package: "fixture", Kind: "type", Symbol: "Grouped", File: "valid/a_claims.go", MarkerLine: 28, StartLine: 29, EndLine: 29},
		{ID: "multiple-precondition-1", Marker: MarkerPrecondition, Text: "first claim", Package: "fixture", Kind: "func", Symbol: "Multiple", File: "valid/a_claims.go", MarkerLine: 33, StartLine: 32, EndLine: 36},
		{ID: "multiple-postcondition-1", Marker: MarkerPostcondition, Text: "second claim\nwith a continuation", Package: "fixture", Kind: "func", Symbol: "Multiple", File: "valid/a_claims.go", MarkerLine: 34, StartLine: 32, EndLine: 36},
		{ID: "continuationonly-invariant-1", Marker: MarkerInvariant, Text: "an empty marker can use a continuation", Package: "fixture", Kind: "func", Symbol: "ContinuationOnly", File: "valid/a_claims.go", MarkerLine: 38, StartLine: 40, EndLine: 40},
		{ID: "physicallines-postcondition-1", Marker: MarkerPostcondition, Text: "locations use physical source lines", Package: "fixture", Kind: "func", Symbol: "PhysicalLines", File: "valid/a_claims.go", MarkerLine: 49, StartLine: 52, EndLine: 52},
		{ID: "fixture-invariant-1", Marker: MarkerInvariant, Text: "a free-standing claim is associated with the package", Package: "fixture", Kind: "package", Symbol: "fixture", File: "valid/b_subjects.go", MarkerLine: 3, StartLine: 1, EndLine: 1},
		{ID: "variable-precondition-1", Marker: MarkerPrecondition, Text: "variables can be direct subjects", Package: "fixture", Kind: "var", Symbol: "Variable", File: "valid/b_subjects.go", MarkerLine: 7, StartLine: 8, EndLine: 8},
		{ID: "limit-invariant-1", Marker: MarkerInvariant, Text: "constants can be direct subjects", Package: "fixture", Kind: "const", Symbol: "Limit", File: "valid/b_subjects.go", MarkerLine: 11, StartLine: 12, EndLine: 12},
		{ID: "fixture-postcondition-1", Marker: MarkerPostcondition, Text: "a grouped declaration comment falls back to the package", Package: "fixture", Kind: "package", Symbol: "fixture", File: "valid/b_subjects.go", MarkerLine: 15, StartLine: 1, EndLine: 1},
		{ID: "trailing-invariant-1", Marker: MarkerInvariant, Text: "trailing value comments use the value as their subject", Package: "fixture", Kind: "var", Symbol: "Trailing", File: "valid/b_subjects.go", MarkerLine: 20, StartLine: 20, EndLine: 20},
		{ID: "invalidconditionboundary-precondition-1", Marker: MarkerPrecondition, Text: "valid claim before an invalid condition", Package: "fixture", Kind: "func", Symbol: "InvalidConditionBoundary", File: "valid/b_subjects.go", MarkerLine: 29, StartLine: 28, EndLine: 33},
		{ID: "testhelper-postcondition-1", Marker: MarkerPostcondition, Text: "test files with claims are included", Package: "fixture", Kind: "func", Symbol: "TestHelper", File: "valid/c_fixture_test.go", MarkerLine: 3, StartLine: 4, EndLine: 4},
		{ID: "asserted-assertion-1", Marker: MarkerAssertion, Text: "value is available at this exact program point", Package: "fixture", Kind: "func", Symbol: "Asserted", File: "valid/d_assertions.go", MarkerLine: 4, StartLine: 3, EndLine: 6},
	}
	if !reflect.DeepEqual(result.Claims, wantClaims) {
		t.Fatalf("Scan() claims mismatch\n got: %#v\nwant: %#v", result.Claims, wantClaims)
	}

	wantDiagnostics := []Diagnostic{
		{File: "valid/a_claims.go", Line: 42, Message: "PRECONDITION claim is empty"},
		{File: "valid/a_claims.go", Line: 46, Message: "INVARIANT claim is empty"},
	}
	if !reflect.DeepEqual(result.Diagnostics, wantDiagnostics) {
		t.Fatalf("Scan() diagnostics mismatch\n got: %#v\nwant: %#v", result.Diagnostics, wantDiagnostics)
	}
}

func TestClaimIDsAreStableAcrossScopesAndEdits(t *testing.T) {
	t.Parallel()
	repoRoot := t.TempDir()
	scope := filepath.Join(repoRoot, "queue")
	mustMkdir(t, scope)
	filename := filepath.Join(scope, "buffer.go")
	const source = `package queue

// INVARIANT buffer-single-owner: exactly one owner
// even when reused
type Buffer struct{}

// PRECONDITION: size is positive
// PRECONDITION valid-size: size fits in memory
// PRECONDITION: size is bounded
func NewBuffer(size int) *Buffer { return &Buffer{} }

// POSTCONDITION closed: closed on return
// POSTCONDITION: resources are released
func (b *Buffer) Close() {
    // ASSERTION:
    // cleanup is about to start
}

// INVARIANT: independent claim
func Other() {}
`
	mustWriteFile(t, filename, source)
	want := []string{"buffer-single-owner", "newbuffer-precondition-1", "valid-size", "newbuffer-precondition-3", "closed", "buffer-close-postcondition-2", "buffer-close-assertion-1", "other-invariant-1"}
	checkIDs := func(scope string, expected []string) {
		t.Helper()
		result, err := Scan(repoRoot, scope)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Diagnostics) != 0 {
			t.Fatalf("unexpected diagnostics: %#v", result.Diagnostics)
		}
		var ids []string
		for _, claim := range result.Claims {
			if claim.File == "queue/buffer.go" {
				ids = append(ids, claim.ID)
			}
		}
		if !reflect.DeepEqual(ids, expected) {
			t.Fatalf("IDs for %s = %v, want %v", scope, ids, expected)
		}
	}
	checkIDs(filename, want)
	// The same symbol in another file must not influence generated IDs.
	mustWriteFile(t, filepath.Join(repoRoot, "other.go"), source)
	checkIDs(scope, want)
	checkIDs(repoRoot, want)
	// Moving the claims by adding lines and editing prose preserves their IDs.
	edited := "// unrelated header\n\n" + strings.ReplaceAll(source, "size is positive", "size is greater than zero")
	mustWriteFile(t, filename, edited)
	checkIDs(filename, want)
	// Giving an earlier claim a name must not renumber later unnamed claims.
	mustWriteFile(t, filename, strings.Replace(edited, "// PRECONDITION:", "// PRECONDITION positive-size:", 1))
	want[1] = "positive-size"
	checkIDs(filename, want)
}

func TestNamedClaimContinuationsAndInvalidBoundaries(t *testing.T) {
	t.Parallel()
	repoRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(repoRoot, "claims.go"), `package example

// INVARIANT named: first line
// continuation
// POSTCONDITION:
// second claim
// ASSERTION point: third claim
//
// excluded after blank comment
func Example() {}

// INVARIANT empty:
func Empty() {}

func Boundary() {
    // PRECONDITION before-invalid: valid claim
    //postcondition invalid: ignored spelling
    // must not be appended
    // ASSERTION after-invalid: next valid claim
}
`)
	result, err := Scan(repoRoot, repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ id, text string }{
		{"named", "first line\ncontinuation"},
		{"example-postcondition-1", "second claim"},
		{"point", "third claim"},
		{"before-invalid", "valid claim"},
		{"after-invalid", "next valid claim"},
	}
	if len(result.Claims) != len(want) {
		t.Fatalf("claims = %#v, want %d", result.Claims, len(want))
	}
	for index, expected := range want {
		claim := result.Claims[index]
		if claim.ID != expected.id || claim.Text != expected.text {
			t.Errorf("claim %d = %#v, want %v", index, claim, expected)
		}
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Message != "INVARIANT claim is empty" {
		t.Fatalf("diagnostics = %#v, want one empty-claim warning", result.Diagnostics)
	}
}

func TestScanDoesNotTraverseSymlinks(t *testing.T) {
	t.Parallel()

	repoRoot := t.TempDir()
	scope := filepath.Join(repoRoot, "scope")
	outside := filepath.Join(repoRoot, "outside")
	mustMkdir(t, scope)
	mustMkdir(t, outside)
	mustWriteFile(t, filepath.Join(scope, "included.go"), "package sample\n\n// PRECONDITION: included\nfunc Included() {}\n")
	mustWriteFile(t, filepath.Join(outside, "excluded.go"), "package sample\n\n// POSTCONDITION: excluded\nfunc Excluded() {}\n")

	if err := os.Symlink(outside, filepath.Join(scope, "linked_directory")); err != nil {
		t.Skipf("cannot create directory symlink: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "excluded.go"), filepath.Join(scope, "linked_file.go")); err != nil {
		t.Skipf("cannot create file symlink: %v", err)
	}

	result, err := Scan(repoRoot, scope)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(result.Claims) != 1 || result.Claims[0].Symbol != "Included" {
		t.Fatalf("Scan() claims = %#v, want only Included", result.Claims)
	}

	_, err = Scan(repoRoot, filepath.Join(scope, "linked_directory"))
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("Scan() symlink scope error = %v, want symbolic-link error", err)
	}
}

func TestScanAcceptsSingleGoFile(t *testing.T) {
	t.Parallel()

	repoRoot := t.TempDir()
	selected := filepath.Join(repoRoot, "selected.go")
	mustWriteFile(t, selected, "package sample\n\n// INVARIANT: selected\nfunc Selected() {}\n")
	mustWriteFile(t, filepath.Join(repoRoot, "other.go"), "package sample\n\n// POSTCONDITION: not selected\nfunc Other() {}\n")

	result, err := Scan(repoRoot, selected)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(result.Claims) != 1 || result.Claims[0].Marker != MarkerInvariant || result.Claims[0].Symbol != "Selected" {
		t.Fatalf("Scan() claims = %#v, want only Selected", result.Claims)
	}
}

func TestScanContextHonorsCancellation(t *testing.T) {
	t.Parallel()

	repoRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(repoRoot, "selected.go"), "package sample\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ScanContext(ctx, repoRoot, repoRoot)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ScanContext() error = %v, want context cancellation", err)
	}
}

func TestScanContextStopsDuringTraversal(t *testing.T) {
	t.Parallel()

	repoRoot := t.TempDir()
	scope := filepath.Join(repoRoot, "scope")
	mustMkdir(t, scope)
	info, err := os.Stat(scope)
	if err != nil {
		t.Fatal(err)
	}
	entry := fs.FileInfoToDirEntry(info)
	started := make(chan struct{})
	release := make(chan struct{})
	walk := func(_ string, visit fs.WalkDirFunc) error {
		close(started)
		<-release
		return visit(scope, entry, nil)
	}

	ctx, cancel := context.WithCancel(context.Background())
	type scanResult struct {
		err error
	}
	done := make(chan scanResult, 1)
	go func() {
		_, err := scanContextWithWalk(ctx, repoRoot, scope, walk)
		done <- scanResult{err: err}
	}()
	<-started
	startedAt := time.Now()
	cancel()
	close(release)

	select {
	case result := <-done:
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("scanContextWithWalk() error = %v, want context cancellation", result.err)
		}
		if elapsed := time.Since(startedAt); elapsed > time.Second {
			t.Fatalf("scan took %s to stop after cancellation", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("scan did not stop within one second of cancellation")
	}
}

func TestScanRejectsInvalidPaths(t *testing.T) {
	t.Parallel()

	repoRoot := t.TempDir()
	scope := filepath.Join(repoRoot, "scope")
	mustMkdir(t, scope)
	fileScope := filepath.Join(repoRoot, "source.txt")
	mustWriteFile(t, fileScope, "package sample\n")
	outside := t.TempDir()

	tests := []struct {
		name     string
		root     string
		scope    string
		contains string
	}{
		{name: "relative root", root: "relative", scope: scope, contains: "repository root must be an absolute path"},
		{name: "relative scope", root: repoRoot, scope: "relative", contains: "scope must be an absolute path"},
		{name: "outside root", root: repoRoot, scope: outside, contains: "outside repository root"},
		{name: "unsupported file scope", root: repoRoot, scope: fileScope, contains: "expected a .go file"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := Scan(test.root, test.scope)
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("Scan() error = %v, want an error containing %q", err, test.contains)
			}
		})
	}
}

func TestScanSkipsMalformedFiles(t *testing.T) {
	t.Parallel()

	repoRoot := t.TempDir()
	mustWriteFile(t, filepath.Join(repoRoot, "broken.go"), "package sample\n\n// PRECONDITION: malformed files are skipped\nfunc Broken( {\n")
	mustWriteFile(t, filepath.Join(repoRoot, "valid.go"), "package sample\n\n// POSTCONDITION: valid files are still scanned\nfunc Valid() {}\n")

	result, err := Scan(repoRoot, repoRoot)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(result.Claims) != 1 || result.Claims[0].Symbol != "Valid" {
		t.Fatalf("Scan() claims = %#v, want the claim from valid.go", result.Claims)
	}
	if len(result.Diagnostics) != 1 {
		t.Fatalf("Scan() diagnostics = %#v, want one malformed-file warning", result.Diagnostics)
	}
	diagnostic := result.Diagnostics[0]
	if diagnostic.File != "broken.go" || diagnostic.Line != 4 ||
		!strings.Contains(diagnostic.Message, "skipping malformed Go file") ||
		!strings.Contains(diagnostic.Message, "expected") {
		t.Fatalf("malformed-file diagnostic = %#v", diagnostic)
	}
}

func absoluteFixturePath(t *testing.T, path string) string {
	t.Helper()
	sourceRoot, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("filepath.Abs(%q): %v", path, err)
	}

	// Discovery claimionally skips symlinks. Materialize the fixtures so this
	// test exercises the same real-file traversal as ordinary files.
	destinationRoot := t.TempDir()
	err = filepath.WalkDir(sourceRoot, func(source string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(sourceRoot, source)
		if err != nil {
			return err
		}
		destination := filepath.Join(destinationRoot, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		contents, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, contents, 0o644)
	})
	if err != nil {
		t.Fatalf("materialize fixtures from %q: %v", sourceRoot, err)
	}
	return destinationRoot
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q): %v", path, err)
	}
}

func mustWriteFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q): %v", path, err)
	}
}
