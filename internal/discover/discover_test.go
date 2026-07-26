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
	const want = `{"claims":[{"marker":"INVARIANT","text":"claim","package":"sample","kind":"func","symbol":"Example","file":"sample.go","marker_line":1,"start_line":2,"end_line":3}],"diagnostics":[{"file":"sample.go","line":4,"message":"message"}]}`
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
			gotMarker, gotText, gotOK := markerText(test.raw)
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
		{Marker: MarkerInvariant, Text: "package documentation is associated with the package", Package: "fixture", Kind: "package", Symbol: "fixture", File: "valid/a_claims.go", MarkerLine: 1, StartLine: 2, EndLine: 2},
		{Marker: MarkerPrecondition, Text: "function documentation is associated with the function", Package: "fixture", Kind: "func", Symbol: "Documented", File: "valid/a_claims.go", MarkerLine: 4, StartLine: 5, EndLine: 5},
		{Marker: MarkerInvariant, Text: "claims in a function body are associated with the function", Package: "fixture", Kind: "func", Symbol: "InBody", File: "valid/a_claims.go", MarkerLine: 8, StartLine: 7, EndLine: 10},
		{Marker: MarkerPostcondition, Text: "claims in a method body are associated with the method", Package: "fixture", Kind: "method", Symbol: "(*Cache).Get", File: "valid/a_claims.go", MarkerLine: 15, StartLine: 14, EndLine: 17},
		{Marker: MarkerInvariant, Text: "named types remain supported", Package: "fixture", Kind: "type", Symbol: "Named", File: "valid/a_claims.go", MarkerLine: 19, StartLine: 20, EndLine: 25},
		{Marker: MarkerInvariant, Text: "field documentation is associated with the field", Package: "fixture", Kind: "field", Symbol: "Named.Value", File: "valid/a_claims.go", MarkerLine: 21, StartLine: 22, EndLine: 22},
		{Marker: MarkerInvariant, Text: "trailing field comments are associated with the field", Package: "fixture", Kind: "field", Symbol: "Named.Count", File: "valid/a_claims.go", MarkerLine: 24, StartLine: 24, EndLine: 24},
		{Marker: MarkerPrecondition, Text: "grouped type specifications are associated with the named type", Package: "fixture", Kind: "type", Symbol: "Grouped", File: "valid/a_claims.go", MarkerLine: 28, StartLine: 29, EndLine: 29},
		{Marker: MarkerPrecondition, Text: "first claim", Package: "fixture", Kind: "func", Symbol: "Multiple", File: "valid/a_claims.go", MarkerLine: 33, StartLine: 32, EndLine: 36},
		{Marker: MarkerPostcondition, Text: "second claim\nwith a continuation", Package: "fixture", Kind: "func", Symbol: "Multiple", File: "valid/a_claims.go", MarkerLine: 34, StartLine: 32, EndLine: 36},
		{Marker: MarkerInvariant, Text: "an empty marker can use a continuation", Package: "fixture", Kind: "func", Symbol: "ContinuationOnly", File: "valid/a_claims.go", MarkerLine: 38, StartLine: 40, EndLine: 40},
		{Marker: MarkerPostcondition, Text: "locations use physical source lines", Package: "fixture", Kind: "func", Symbol: "PhysicalLines", File: "valid/a_claims.go", MarkerLine: 49, StartLine: 52, EndLine: 52},
		{Marker: MarkerInvariant, Text: "a free-standing claim is associated with the package", Package: "fixture", Kind: "package", Symbol: "fixture", File: "valid/b_subjects.go", MarkerLine: 3, StartLine: 1, EndLine: 1},
		{Marker: MarkerPrecondition, Text: "variables can be direct subjects", Package: "fixture", Kind: "var", Symbol: "Variable", File: "valid/b_subjects.go", MarkerLine: 7, StartLine: 8, EndLine: 8},
		{Marker: MarkerInvariant, Text: "constants can be direct subjects", Package: "fixture", Kind: "const", Symbol: "Limit", File: "valid/b_subjects.go", MarkerLine: 11, StartLine: 12, EndLine: 12},
		{Marker: MarkerPostcondition, Text: "a grouped declaration comment falls back to the package", Package: "fixture", Kind: "package", Symbol: "fixture", File: "valid/b_subjects.go", MarkerLine: 15, StartLine: 1, EndLine: 1},
		{Marker: MarkerInvariant, Text: "trailing value comments use the value as their subject", Package: "fixture", Kind: "var", Symbol: "Trailing", File: "valid/b_subjects.go", MarkerLine: 20, StartLine: 20, EndLine: 20},
		{Marker: MarkerPrecondition, Text: "valid claim before an invalid condition", Package: "fixture", Kind: "func", Symbol: "InvalidConditionBoundary", File: "valid/b_subjects.go", MarkerLine: 29, StartLine: 28, EndLine: 33},
		{Marker: MarkerPostcondition, Text: "test files with claims are included", Package: "fixture", Kind: "func", Symbol: "TestHelper", File: "valid/c_fixture_test.go", MarkerLine: 3, StartLine: 4, EndLine: 4},
		{Marker: MarkerAssertion, Text: "value is available at this exact program point", Package: "fixture", Kind: "func", Symbol: "Asserted", File: "valid/d_assertions.go", MarkerLine: 4, StartLine: 3, EndLine: 6},
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
