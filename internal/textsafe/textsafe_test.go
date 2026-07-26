package textsafe

import "testing"

func TestSanitize(t *testing.T) {
	t.Parallel()

	input := "first\nsecond\t\x1b]52;clipboard\a café"
	if got, want := Sanitize(input), "first\nsecond    \\u{1b}]52;clipboard\\u{7} café"; got != want {
		t.Fatalf("Sanitize() = %q, want %q", got, want)
	}
	if got, want := SingleLine(input), "first\\nsecond\\t\\u{1b}]52;clipboard\\u{7} café"; got != want {
		t.Fatalf("SingleLine() = %q, want %q", got, want)
	}
}
