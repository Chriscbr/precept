// Package prompt renders the verifier instructions and validates the agent's
// normalized response.
package prompt

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"
	"text/template"
)

// Data describes one claim to the verifier.
type Data struct {
	RepositoryRoot   string
	Scope            string
	Marker           string
	Claim            string
	Package          string
	SubjectKind      string
	Symbol           string
	File             string
	MarkerLine       int
	SubjectStartLine int
	SubjectEndLine   int
	AppendedSections []string
}

// Verdict is the normalized result of verifying a claim.
type Verdict string

const (
	VerdictHolds        Verdict = "holds"
	VerdictViolated     Verdict = "violated"
	VerdictInconclusive Verdict = "inconclusive"
	VerdictError        Verdict = "error"
)

// Evidence identifies source code that supports the verdict.
type Evidence struct {
	File      string `json:"file"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Reason    string `json:"reason"`
}

// Result is the small, harness-independent payload returned by a verifier.
type Result struct {
	Verdict        Verdict    `json:"verdict"`
	Summary        string     `json:"summary"`
	Evidence       []Evidence `json:"evidence"`
	Counterexample string     `json:"counterexample"`
}

//go:embed verify.md
var verifierMarkdown string

//go:embed result.schema.json
var resultSchema []byte

var verifierTemplate = template.Must(template.New("verify.md").Option("missingkey=error").Parse(verifierMarkdown))

type templateData struct {
	Data
	ResultSchema string
}

// Render renders the embedded verifier prompt for one claim.
func Render(data Data) (string, error) {
	if err := validateData(data); err != nil {
		return "", err
	}

	var rendered bytes.Buffer
	err := verifierTemplate.Execute(&rendered, templateData{
		Data:         data,
		ResultSchema: string(resultSchema),
	})
	if err != nil {
		return "", fmt.Errorf("render verifier prompt: %w", err)
	}
	return rendered.String(), nil
}

// SchemaBytes returns a defensive copy of the embedded result JSON Schema.
func SchemaBytes() []byte {
	return bytes.Clone(resultSchema)
}

// ParseResult strictly decodes and validates a normalized verifier result.
func ParseResult(payload []byte) (Result, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()

	var wire struct {
		Verdict        *Verdict    `json:"verdict"`
		Summary        *string     `json:"summary"`
		Evidence       *[]Evidence `json:"evidence"`
		Counterexample *string     `json:"counterexample"`
	}
	if err := decoder.Decode(&wire); err != nil {
		return Result{}, fmt.Errorf("decode verifier result: %w", err)
	}

	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Result{}, fmt.Errorf("decode verifier result: unexpected trailing JSON value")
		}
		return Result{}, fmt.Errorf("decode verifier result: %w", err)
	}
	for _, required := range []struct {
		name    string
		present bool
	}{
		{name: "verdict", present: wire.Verdict != nil},
		{name: "summary", present: wire.Summary != nil},
		{name: "evidence", present: wire.Evidence != nil},
		{name: "counterexample", present: wire.Counterexample != nil},
	} {
		if !required.present {
			return Result{}, fmt.Errorf("decode verifier result: required field %q is missing or null", required.name)
		}
	}

	result := Result{
		Verdict:        *wire.Verdict,
		Summary:        *wire.Summary,
		Evidence:       *wire.Evidence,
		Counterexample: *wire.Counterexample,
	}
	result.normalize()
	if err := result.Validate(); err != nil {
		return Result{}, err
	}
	return result, nil
}

// Validate checks the semantic constraints that are stricter than basic JSON
// decoding. It does not mutate the result; ParseResult normalizes whitespace
// before calling it.
func (result Result) Validate() error {
	switch result.Verdict {
	case VerdictHolds, VerdictViolated, VerdictInconclusive, VerdictError:
	default:
		return fmt.Errorf("validate verifier result: unsupported verdict %q", result.Verdict)
	}

	if strings.TrimSpace(result.Summary) == "" {
		return fmt.Errorf("validate verifier result: summary must not be empty")
	}
	if len(result.Evidence) == 0 {
		return fmt.Errorf("validate verifier result: at least one evidence item is required")
	}
	for index, evidence := range result.Evidence {
		if err := validateRepositoryPath(evidence.File); err != nil {
			return fmt.Errorf("validate verifier result: evidence %d file: %w", index, err)
		}
		if evidence.StartLine < 1 {
			return fmt.Errorf("validate verifier result: evidence %d start line must be positive", index)
		}
		if evidence.EndLine < evidence.StartLine {
			return fmt.Errorf("validate verifier result: evidence %d end line must be at or after start line", index)
		}
		if strings.TrimSpace(evidence.Reason) == "" {
			return fmt.Errorf("validate verifier result: evidence %d reason must not be empty", index)
		}
	}

	if result.Verdict != VerdictViolated && strings.TrimSpace(result.Counterexample) != "" {
		return fmt.Errorf("validate verifier result: counterexample is only allowed for a violated claim")
	}
	return nil
}

func (result *Result) normalize() {
	result.Summary = strings.TrimSpace(result.Summary)
	result.Counterexample = strings.TrimSpace(result.Counterexample)
	for index := range result.Evidence {
		result.Evidence[index].File = strings.TrimSpace(result.Evidence[index].File)
		result.Evidence[index].Reason = strings.TrimSpace(result.Evidence[index].Reason)
	}
}

func validateData(data Data) error {
	required := []struct {
		name  string
		value string
	}{
		{name: "repository root", value: data.RepositoryRoot},
		{name: "scope", value: data.Scope},
		{name: "marker", value: data.Marker},
		{name: "claim", value: data.Claim},
		{name: "package", value: data.Package},
		{name: "subject kind", value: data.SubjectKind},
		{name: "symbol", value: data.Symbol},
	}
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("render verifier prompt: %s must not be empty", field.name)
		}
	}
	if data.Marker != "INVARIANT" &&
		data.Marker != "PRECONDITION" &&
		data.Marker != "POSTCONDITION" &&
		data.Marker != "ASSERTION" {
		return fmt.Errorf("render verifier prompt: unsupported marker %q", data.Marker)
	}
	if err := validateRepositoryPath(data.File); err != nil {
		return fmt.Errorf("render verifier prompt: file: %w", err)
	}
	if data.MarkerLine < 1 {
		return fmt.Errorf("render verifier prompt: marker line must be positive")
	}
	if data.SubjectStartLine < 1 {
		return fmt.Errorf("render verifier prompt: subject start line must be positive")
	}
	if data.SubjectEndLine < data.SubjectStartLine {
		return fmt.Errorf("render verifier prompt: subject end line must be at or after start line")
	}
	return nil
}

func validateRepositoryPath(file string) error {
	if file == "" {
		return fmt.Errorf("must not be empty")
	}
	if strings.Contains(file, "\\") {
		return fmt.Errorf("must use forward slashes")
	}
	if strings.HasPrefix(file, "/") {
		return fmt.Errorf("must be repository-relative")
	}
	cleaned := path.Clean(file)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("must remain inside the repository")
	}
	if cleaned != file {
		return fmt.Errorf("must be a clean repository-relative path")
	}
	return nil
}
