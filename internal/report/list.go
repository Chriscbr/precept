package report

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/Chriscbr/precept/internal/discover"
	"github.com/Chriscbr/precept/internal/textsafe"
)

// WriteListText renders discovered claims with the same symbol formatting and
// terminal styling policy as verification reports. Compact output lists only
// claim types, symbols, and source locations.
func WriteListText(writer io.Writer, claims []discover.Claim, compact bool) error {
	style := textStyle{enabled: shouldStyle(writer, os.LookupEnv)}
	var err error
	if compact {
		err = writeCompactClaims(writer, claims, style)
	} else {
		err = writeListClaims(writer, claims, style)
	}
	if err != nil {
		return err
	}
	files := make(map[string]struct{})
	for _, claim := range claims {
		files[claim.File] = struct{}{}
	}
	claimNoun, fileNoun := "claims", "files"
	if len(claims) == 1 {
		claimNoun = "claim"
	}
	if len(files) == 1 {
		fileNoun = "file"
	}
	summary := fmt.Sprintf("%d %s in %d %s", len(claims), claimNoun, len(files), fileNoun)
	if _, err := fmt.Fprintln(writer, style.gray(summary)); err != nil {
		return fmt.Errorf("write claim summary: %w", err)
	}
	return nil
}

func writeListClaims(writer io.Writer, claims []discover.Claim, style textStyle) error {
	for _, claim := range claims {
		location := fmt.Sprintf("(%s:%d)", textsafe.SingleLine(claim.File), claim.MarkerLine)
		if _, err := fmt.Fprintf(writer, "%s %s %s\n",
			style.bold(textsafe.SingleLine(displaySymbol(claim))),
			style.color(ansiCyan, "["+textsafe.SingleLine(string(claim.Marker))+"]"),
			style.gray(location),
		); err != nil {
			return fmt.Errorf("write claim header: %w", err)
		}
		for _, line := range strings.Split(claim.Text, "\n") {
			if _, err := fmt.Fprintf(writer, "  %s\n", textsafe.SingleLine(line)); err != nil {
				return fmt.Errorf("write claim text: %w", err)
			}
		}
		if _, err := io.WriteString(writer, "\n"); err != nil {
			return fmt.Errorf("write claim separator: %w", err)
		}
	}
	return nil
}

func writeCompactClaims(writer io.Writer, claims []discover.Claim, style textStyle) error {
	if len(claims) == 0 {
		return nil
	}
	markerWidth, symbolWidth := len("KIND"), len("SYMBOL")
	for _, claim := range claims {
		markerWidth = max(markerWidth, utf8.RuneCountInString(textsafe.SingleLine(string(claim.Marker))))
		symbolWidth = max(symbolWidth, utf8.RuneCountInString(textsafe.SingleLine(displaySymbol(claim))))
	}
	header := fmt.Sprintf("%-*s  %-*s  SOURCE", markerWidth, "KIND", symbolWidth, "SYMBOL")
	if _, err := fmt.Fprintln(writer, style.gray(header)); err != nil {
		return fmt.Errorf("write compact claim header: %w", err)
	}
	for _, claim := range claims {
		// Pad before styling so ANSI sequences do not affect column alignment.
		marker := fmt.Sprintf("%-*s", markerWidth, textsafe.SingleLine(string(claim.Marker)))
		symbol := fmt.Sprintf("%-*s", symbolWidth, textsafe.SingleLine(displaySymbol(claim)))
		location := fmt.Sprintf("%s:%d", textsafe.SingleLine(claim.File), claim.MarkerLine)
		if _, err := fmt.Fprintf(writer, "%s  %s  %s\n",
			style.color(ansiCyan, marker), style.bold(symbol), style.gray(location),
		); err != nil {
			return fmt.Errorf("write compact claim: %w", err)
		}
	}
	if _, err := io.WriteString(writer, "\n"); err != nil {
		return fmt.Errorf("write claim separator: %w", err)
	}
	return nil
}
