package report

import (
	"fmt"
	"io"

	"github.com/Chriscbr/precept/internal/textsafe"
)

// WriteError prints a command error to stderr using a red prefix when terminal
// color is enabled. Joined errors each receive their own line and prefix.
func WriteError(writer io.Writer, err error) error {
	return writeError(writer, err, newTextStyle(writer, ""))
}

func writeError(writer io.Writer, err error, style textStyle) error {
	if err == nil {
		return nil
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, item := range joined.Unwrap() {
			if writeErr := writeError(writer, item, style); writeErr != nil {
				return writeErr
			}
		}
		return nil
	}
	_, writeErr := fmt.Fprintf(writer, "%s %s\n", style.color(ansiRed, "error:"), textsafe.SingleLine(err.Error()))
	return writeErr
}
