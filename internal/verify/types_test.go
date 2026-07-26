package verify

import "testing"

func TestResultValidate(t *testing.T) {
	t.Parallel()

	valid := Result{
		Verdict: VerdictHolds,
		Summary: "the implementation clamps both bounds",
		Evidence: []Evidence{{
			File:      "example/clamp.go",
			StartLine: 12,
			EndLine:   14,
			Reason:    "both bounds are applied",
		}},
	}

	tests := []struct {
		name    string
		mutate  func(*Result)
		wantErr bool
	}{
		{name: "valid"},
		{name: "claim type error", mutate: func(result *Result) { result.Verdict = VerdictError }},
		{name: "unknown verdict", mutate: func(result *Result) { result.Verdict = "maybe" }, wantErr: true},
		{name: "empty summary", mutate: func(result *Result) { result.Summary = " \t" }, wantErr: true},
		{name: "empty evidence", mutate: func(result *Result) { result.Evidence = nil }, wantErr: true},
		{name: "empty evidence file", mutate: func(result *Result) { result.Evidence[0].File = "" }, wantErr: true},
		{name: "absolute evidence file", mutate: func(result *Result) { result.Evidence[0].File = "/tmp/file.go" }, wantErr: true},
		{name: "escaping evidence file", mutate: func(result *Result) { result.Evidence[0].File = "../file.go" }, wantErr: true},
		{name: "unclean evidence file", mutate: func(result *Result) { result.Evidence[0].File = "example/../file.go" }, wantErr: true},
		{name: "backslash evidence file", mutate: func(result *Result) { result.Evidence[0].File = `domains\\file.go` }, wantErr: true},
		{name: "non-positive evidence start", mutate: func(result *Result) { result.Evidence[0].StartLine = 0 }, wantErr: true},
		{name: "evidence ends before start", mutate: func(result *Result) { result.Evidence[0].EndLine = 11 }, wantErr: true},
		{name: "empty evidence reason", mutate: func(result *Result) { result.Evidence[0].Reason = "" }, wantErr: true},
		{name: "counterexample for holds", mutate: func(result *Result) { result.Counterexample = "input -1" }, wantErr: true},
		{
			name: "optional counterexample for violation",
			mutate: func(result *Result) {
				result.Verdict = VerdictViolated
				result.Counterexample = ""
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := valid
			got.Evidence = append([]Evidence(nil), valid.Evidence...)
			if test.mutate != nil {
				test.mutate(&got)
			}
			if err := got.Validate(); (err != nil) != test.wantErr {
				t.Fatalf("Validate() error = %v, wantErr = %t", err, test.wantErr)
			}
		})
	}
}
