package report

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestWriteError(t *testing.T) {
	t.Parallel()
	err := errors.Join(
		errors.New(`claim ID "shared" is used twice in the same file (a.go)`),
		errors.New("untrusted\nerror: spoof\x1b[31m"),
	)
	const first = `claim ID "shared" is used twice in the same file (a.go)`
	const second = `untrusted\nerror: spoof\u{1b}[31m`
	for _, styled := range []bool{false, true} {
		var output bytes.Buffer
		if writeErr := writeError(&output, err, textStyle{enabled: styled}); writeErr != nil {
			t.Fatal(writeErr)
		}
		prefix := "error:"
		if styled {
			prefix = "\x1b[31merror:\x1b[m"
		}
		want := prefix + " " + first + "\n" + prefix + " " + second + "\n"
		if output.String() != want {
			t.Fatalf("styled=%t: got %q, want %q", styled, output.String(), want)
		}
	}
	var plain bytes.Buffer
	if err := WriteError(&plain, errors.New(first)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain.String(), "\x1b") || plain.String() != "error: "+first+"\n" {
		t.Fatalf("redirected error output = %q", plain.String())
	}
}
