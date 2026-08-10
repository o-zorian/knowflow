package chat

import (
	"strconv"
	"strings"
	"unicode"
)

// CitationFilter validates numeric citation markers while preserving streaming.
// It buffers only an incomplete bracket marker across model deltas.
type CitationFilter struct {
	maximum int
	pending string
	seen    map[int]bool
	numbers []int
}

func NewCitationFilter(maximum int) *CitationFilter {
	return &CitationFilter{maximum: maximum, seen: make(map[int]bool)}
}

func (f *CitationFilter) Feed(delta string) string {
	f.pending += delta
	return f.consume(false)
}

func (f *CitationFilter) Close() string { return f.consume(true) }

func (f *CitationFilter) Numbers() []int { return append([]int(nil), f.numbers...) }

func (f *CitationFilter) consume(final bool) string {
	var output strings.Builder
	for f.pending != "" {
		open := strings.IndexByte(f.pending, '[')
		if open < 0 {
			output.WriteString(f.pending)
			f.pending = ""
			break
		}
		output.WriteString(f.pending[:open])
		f.pending = f.pending[open:]
		closeIndex := strings.IndexByte(f.pending, ']')
		if closeIndex < 0 {
			if final || len([]rune(f.pending)) > 12 {
				output.WriteString(f.pending[:1])
				f.pending = f.pending[1:]
				continue
			}
			break
		}
		marker := f.pending[:closeIndex+1]
		inside := marker[1 : len(marker)-1]
		if numeric(inside) {
			number, _ := strconv.Atoi(inside)
			if number >= 1 && number <= f.maximum {
				output.WriteString(marker)
				if !f.seen[number] {
					f.seen[number] = true
					f.numbers = append(f.numbers, number)
				}
			}
		} else {
			output.WriteString(marker)
		}
		f.pending = f.pending[closeIndex+1:]
	}
	return output.String()
}

func numeric(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if !unicode.IsDigit(char) || char > unicode.MaxASCII {
			return false
		}
	}
	return true
}
