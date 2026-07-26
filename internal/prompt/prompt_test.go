package prompt

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

//go:embed testdata/verify.sha256.golden
var verifyGolden string

func TestRenderGolden(t *testing.T) {
	data := validData()
	data.AppendedSections = []string{
		"Assume callers hold the cache lock.",
		"Pay particular attention to zero values.\nThis line belongs to the same section.",
	}

	rendered, err := Render(data)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	want := strings.TrimSpace(verifyGolden)
	digest := sha256.Sum256([]byte(rendered))
	got := hex.EncodeToString(digest[:])
	if got != want {
		t.Fatalf("rendered prompt digest = %s, want %s", got, want)
	}

	for _, fragment := range []string{
		"Repository root: `/repo/project`",
		"Requested scope: `src`",
		"Claim marker: `INVARIANT`",
		"<claim>\nresult is never negative\n</claim>",
		"`PRECONDITION`: the claim is expected to hold when execution enters",
		"`POSTCONDITION`: the claim is expected to hold on every normal return path",
		"`ASSERTION`: the claim is expected to hold at the marker's particular program point",
		"`INVARIANT`: the claim is expected to hold at the boundaries",
		`<additional_context index="0">`,
		"Assume callers hold the cache lock.",
		`<additional_context index="1">`,
		`"inconclusive"`,
		`"error"`,
	} {
		if !strings.Contains(rendered, fragment) {
			t.Errorf("rendered prompt does not contain %q", fragment)
		}
	}
}

func TestRenderAcceptsEveryClaimMarker(t *testing.T) {
	t.Parallel()

	for _, marker := range []string{"INVARIANT", "PRECONDITION", "POSTCONDITION", "ASSERTION"} {
		marker := marker
		t.Run(marker, func(t *testing.T) {
			t.Parallel()
			data := validData()
			data.Marker = marker
			if _, err := Render(data); err != nil {
				t.Fatalf("Render() error = %v", err)
			}
		})
	}
}

func TestRenderRejectsInvalidData(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Data)
		want   string
	}{
		{name: "empty claim", mutate: func(data *Data) { data.Claim = " \t" }, want: "claim must not be empty"},
		{name: "unsupported marker", mutate: func(data *Data) { data.Marker = "invariant" }, want: "unsupported marker"},
		{name: "removed precept marker", mutate: func(data *Data) { data.Marker = "PRECEPT" }, want: "unsupported marker"},
		{name: "absolute file", mutate: func(data *Data) { data.File = "/tmp/source.go" }, want: "repository-relative"},
		{name: "escaping file", mutate: func(data *Data) { data.File = "../source.go" }, want: "inside the repository"},
		{name: "bad marker line", mutate: func(data *Data) { data.MarkerLine = 0 }, want: "marker line must be positive"},
		{name: "bad subject range", mutate: func(data *Data) { data.SubjectEndLine = 11 }, want: "end line must be at or after"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := validData()
			test.mutate(&data)
			_, err := Render(data)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Render() error = %v, want error containing %q", err, test.want)
			}
		})
	}
}

func TestSchemaBytesIsValidAndDefensive(t *testing.T) {
	first := SchemaBytes()
	var schema map[string]any
	if err := json.Unmarshal(first, &schema); err != nil {
		t.Fatalf("SchemaBytes() is not valid JSON: %v", err)
	}
	if schema["type"] != "object" {
		t.Fatalf("schema type = %v, want object", schema["type"])
	}

	first[0] = 'x'
	second := SchemaBytes()
	if second[0] == 'x' {
		t.Fatal("SchemaBytes() returned mutable embedded storage")
	}
}

func TestParseResult(t *testing.T) {
	result, err := ParseResult([]byte(`{
		"verdict":"violated",
		"summary":"  the zero case bypasses the bound  ",
		"evidence":[{"file":" src/value.go ","start_line":12,"end_line":14,"reason":"  early return  "}],
		"counterexample":"  value=0  "
	}`))
	if err != nil {
		t.Fatalf("ParseResult() error = %v", err)
	}
	if result.Verdict != VerdictViolated {
		t.Errorf("Verdict = %q, want %q", result.Verdict, VerdictViolated)
	}
	if result.Summary != "the zero case bypasses the bound" {
		t.Errorf("Summary = %q", result.Summary)
	}
	if result.Evidence[0].File != "src/value.go" || result.Evidence[0].Reason != "early return" {
		t.Errorf("Evidence = %+v", result.Evidence[0])
	}
	if result.Counterexample != "value=0" {
		t.Errorf("Counterexample = %q", result.Counterexample)
	}
}

func TestParseResultAcceptsClaimTypeError(t *testing.T) {
	result, err := ParseResult([]byte(`{
		"verdict":"error",
		"summary":"the marker requires an entry condition, but the claim only describes a return value",
		"evidence":[{"file":"src/value.go","start_line":2,"end_line":4,"reason":"the claim is attached to the function declaration"}],
		"counterexample":""
	}`))
	if err != nil {
		t.Fatalf("ParseResult() error = %v", err)
	}
	if result.Verdict != VerdictError {
		t.Errorf("Verdict = %q, want %q", result.Verdict, VerdictError)
	}
}

func TestParseResultRejectsInvalidPayloads(t *testing.T) {
	valid := `"summary":"summary","evidence":[{"file":"src/value.go","start_line":2,"end_line":3,"reason":"reason"}],"counterexample":""`
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{name: "unknown field", payload: `{"verdict":"holds",` + valid + `,"extra":true}`, want: "unknown field"},
		{name: "trailing value", payload: `{"verdict":"holds",` + valid + `} {}`, want: "trailing JSON value"},
		{name: "missing counterexample", payload: `{"verdict":"holds","summary":"summary","evidence":[{"file":"source.go","start_line":1,"end_line":1,"reason":"reason"}]}`, want: `required field "counterexample"`},
		{name: "bad verdict", payload: `{"verdict":"maybe",` + valid + `}`, want: "unsupported verdict"},
		{name: "empty summary", payload: `{"verdict":"holds","summary":" ","evidence":[{"file":"source.go","start_line":1,"end_line":1,"reason":"reason"}],"counterexample":""}`, want: "summary must not be empty"},
		{name: "no evidence", payload: `{"verdict":"inconclusive","summary":"summary","evidence":[],"counterexample":""}`, want: "at least one evidence"},
		{name: "absolute evidence", payload: `{"verdict":"holds","summary":"summary","evidence":[{"file":"/source.go","start_line":1,"end_line":1,"reason":"reason"}],"counterexample":""}`, want: "repository-relative"},
		{name: "bad evidence range", payload: `{"verdict":"holds","summary":"summary","evidence":[{"file":"source.go","start_line":3,"end_line":2,"reason":"reason"}],"counterexample":""}`, want: "end line must be at or after"},
		{name: "empty evidence reason", payload: `{"verdict":"holds","summary":"summary","evidence":[{"file":"source.go","start_line":1,"end_line":1,"reason":" "}],"counterexample":""}`, want: "reason must not be empty"},
		{name: "counterexample for holds", payload: `{"verdict":"holds","summary":"summary","evidence":[{"file":"source.go","start_line":1,"end_line":1,"reason":"reason"}],"counterexample":"input=0"}`, want: "only allowed for a violated"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseResult([]byte(test.payload))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ParseResult() error = %v, want error containing %q", err, test.want)
			}
		})
	}
}

func TestViolatedResultMayOmitCounterexample(t *testing.T) {
	result := Result{
		Verdict:  VerdictViolated,
		Summary:  "The function always returns zero.",
		Evidence: []Evidence{{File: "source.go", StartLine: 2, EndLine: 4, Reason: "constant return"}},
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func validData() Data {
	return Data{
		RepositoryRoot:   "/repo/project",
		Scope:            "src",
		Marker:           "INVARIANT",
		Claim:            "result is never negative",
		Package:          "clock",
		SubjectKind:      "function",
		Symbol:           "Clamp",
		File:             "src/clock/clamp.go",
		MarkerLine:       10,
		SubjectStartLine: 12,
		SubjectEndLine:   18,
	}
}
