package harness

import (
	"strings"
	"testing"
)

func TestDecodeClaudeOutput(t *testing.T) {
	tests := []struct {
		name        string
		output      string
		wantPayload string
		wantSession string
	}{
		{name: "structured object", output: `{"is_error":false,"session_id":"session-123","structured_output":` + validResultJSON + `}`, wantPayload: validResultJSON, wantSession: "session-123"},
		{name: "result string", output: `{"is_error":false,"result":` + quotedJSON(validResultJSON) + `}`, wantPayload: validResultJSON},
		{name: "direct result", output: validResultJSON, wantPayload: validResultJSON},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := decodeClaudeOutput([]byte(test.output))
			if err != nil {
				t.Fatalf("decodeClaudeOutput() error = %v", err)
			}
			if string(got.payload) != test.wantPayload || got.sessionID != test.wantSession {
				t.Fatalf("decodeClaudeOutput() = %+v, want payload %s and session %q", got, test.wantPayload, test.wantSession)
			}
		})
	}
}

func TestDecodeClaudeOutputErrors(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{name: "invalid JSON", output: `{`, want: "decode JSON output envelope"},
		{name: "agent error", output: `{"is_error":true,"result":"quota exceeded"}`, want: "quota exceeded"},
		{name: "missing result", output: `{"is_error":false}`, want: "did not contain"},
		{name: "empty result", output: `{"is_error":false,"result":" "}`, want: "was empty"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodeClaudeOutput([]byte(test.output))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("decodeClaudeOutput() error = %v, want error containing %q", err, test.want)
			}
		})
	}
}

func TestDecodeClaudeOutputPreservesSessionOnError(t *testing.T) {
	decoded, err := decodeClaudeOutput([]byte(`{"is_error":true,"session_id":"session-on-error","result":"quota exceeded"}`))
	if err == nil || decoded.sessionID != "session-on-error" {
		t.Fatalf("decodeClaudeOutput() = %+v, %v", decoded, err)
	}
}

func quotedJSON(value string) string {
	quoted := strings.Builder{}
	quoted.WriteByte('"')
	for _, character := range value {
		switch character {
		case '\\', '"':
			quoted.WriteByte('\\')
			quoted.WriteRune(character)
		case '\n':
			quoted.WriteString(`\n`)
		default:
			quoted.WriteRune(character)
		}
	}
	quoted.WriteByte('"')
	return quoted.String()
}
