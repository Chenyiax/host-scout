package cli

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// safeStream keeps incomplete UTF-8 sequences across model stream chunks.
type safeStream struct{ pending []byte }

func (s *safeStream) write(chunk string, final bool) string {
	s.pending = append(s.pending, chunk...)
	var out strings.Builder
	for len(s.pending) > 0 {
		if !utf8.FullRune(s.pending) && !final {
			break
		}
		r, size := utf8.DecodeRune(s.pending)
		if r == utf8.RuneError && size == 1 {
			fmt.Fprintf(&out, "\\x%02X", s.pending[0])
		} else if r == '\n' || r == '\t' || (!unicode.IsControl(r) && !unicode.Is(unicode.Cf, r)) {
			out.WriteRune(r)
		} else if r <= 0xff {
			fmt.Fprintf(&out, "\\x%02X", r)
		} else {
			fmt.Fprintf(&out, "\\u%04X", r)
		}
		s.pending = s.pending[size:]
	}
	return out.String()
}

func safeText(value string) string {
	var s safeStream
	return s.write(value, true)
}

func outputPreview(value string) (string, bool) {
	const maxBytes, maxLines = 1024, 8
	truncated := len(value) > maxBytes
	if truncated {
		value = value[:maxBytes]
	}
	lineCount := 0
	for i, r := range value {
		if r == '\n' {
			lineCount++
			if lineCount >= maxLines && i+1 < len(value) {
				value = value[:i+1]
				truncated = true
				break
			}
		}
	}
	return safeText(value), truncated
}
