package harness

import (
	"strings"
	"testing"
)

func TestDecodeCodexOutput(t *testing.T) {
	tests := []struct {
		name        string
		output      string
		wantPayload string
		wantSession string
	}{
		{
			name: "completed item",
			output: `{"type":"thread.started","thread_id":"thread-123"}` + "\n" +
				codexAgentMessage(validResultJSON) + "\n" +
				`{"type":"turn.completed"}`,
			wantPayload: validResultJSON,
			wantSession: "thread-123",
		},
		{
			name:        "direct legacy message",
			output:      `{"type":"agent_message","text":` + quotedJSON(validResultJSON) + `}`,
			wantPayload: validResultJSON,
		},
		{
			name:        "content array",
			output:      `{"type":"item.completed","item":{"type":"agent_message","content":[{"type":"output_text","text":` + quotedJSON(validResultJSON) + `}]}}`,
			wantPayload: validResultJSON,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := decodeCodexOutput([]byte(test.output))
			if err != nil {
				t.Fatalf("decodeCodexOutput() error = %v", err)
			}
			if string(got.payload) != test.wantPayload || got.sessionID != test.wantSession {
				t.Fatalf("decodeCodexOutput() = %+v, want payload %s and session %q", got, test.wantPayload, test.wantSession)
			}
		})
	}
}

func TestDecodeCodexOutputUsesLastMessage(t *testing.T) {
	output := codexAgentMessage(`{"verdict":"inconclusive"}`) + "\n" + codexAgentMessage(validResultJSON)
	got, err := decodeCodexOutput([]byte(output))
	if err != nil {
		t.Fatalf("decodeCodexOutput() error = %v", err)
	}
	if string(got.payload) != validResultJSON {
		t.Fatalf("decodeCodexOutput() = %s, want last message", got)
	}
}

func TestDecodeCodexOutputPreservesSessionOnError(t *testing.T) {
	decoded, err := decodeCodexOutput([]byte("{\"type\":\"thread.started\",\"thread_id\":\"thread-on-error\"}\n{\"type\":\"error\",\"message\":\"quota exceeded\"}"))
	if err == nil || decoded.sessionID != "thread-on-error" {
		t.Fatalf("decodeCodexOutput() = %+v, %v", decoded, err)
	}
}

func TestDecodeCodexOutputErrors(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{name: "invalid JSONL", output: `{`, want: "line 1"},
		{name: "agent error", output: `{"type":"error","message":"quota exceeded"}`, want: "quota exceeded"},
		{name: "missing message", output: `{"type":"turn.completed"}`, want: "did not contain"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodeCodexOutput([]byte(test.output))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("decodeCodexOutput() error = %v, want error containing %q", err, test.want)
			}
		})
	}
}
