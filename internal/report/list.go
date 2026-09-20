package report

import (
	"fmt"
	"io"
	"strings"

	"github.com/Chriscbr/precept/internal/discover"
	"github.com/Chriscbr/precept/internal/textsafe"
	"github.com/charmbracelet/x/ansi"
)

// WriteListText renders discovered claims with the same symbol formatting and
// terminal styling policy as verification reports. Compact output lists only
// claim types, symbols, and source locations.
func WriteListText(writer io.Writer, repositoryRoot string, claims []discover.Claim, compact bool) error {
	style := newTextStyle(writer, repositoryRoot)
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
		if err := writeClaimHeading(writer, claim, "", style); err != nil {
			return fmt.Errorf("write claim: %w", err)
		}
		if err := writeLabeledValue(writer, style, "Claim", textsafe.Sanitize(claim.Text)); err != nil {
			return fmt.Errorf("write claim: %w", err)
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
		markerWidth = max(markerWidth, ansi.StringWidth(textsafe.SingleLine(string(claim.Marker))))
		symbolWidth = max(symbolWidth, ansi.StringWidth(textsafe.SingleLine(displaySymbol(claim))))
	}
	header := padColumn("KIND", markerWidth) + "  " + padColumn("SYMBOL", symbolWidth) + "  SOURCE"
	if _, err := fmt.Fprintln(writer, style.gray(header)); err != nil {
		return fmt.Errorf("write compact claim header: %w", err)
	}
	for _, claim := range claims {
		// Pad before styling so ANSI sequences do not affect column alignment.
		marker := padColumn(textsafe.SingleLine(string(claim.Marker)), markerWidth)
		symbol := padColumn(textsafe.SingleLine(displaySymbol(claim)), symbolWidth)
		location := fmt.Sprintf("%s:%d", textsafe.SingleLine(claim.File), claim.MarkerLine)
		location = style.linkPath(claim.File, location)
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

func padColumn(value string, width int) string {
	return value + strings.Repeat(" ", max(0, width-ansi.StringWidth(value)))
}
