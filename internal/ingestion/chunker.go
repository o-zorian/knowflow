package ingestion

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Chunk struct {
	Index       int
	Content     string
	TokenCount  int
	PageStart   *int
	PageEnd     *int
	HeadingPath string
	ContentHash string
	Metadata    map[string]any
	Embedding   []float32
}

type RecursiveChunker struct{}

func (RecursiveChunker) Chunk(blocks []SourceBlock, chunkSize, overlap int) ([]Chunk, error) {
	if chunkSize <= 0 || overlap < 0 || overlap >= chunkSize {
		return nil, fmt.Errorf("invalid chunk settings: size=%d overlap=%d", chunkSize, overlap)
	}
	var content []rune
	owners := make([]int, 0)
	for index, block := range blocks {
		text := []rune(strings.TrimSpace(block.Content))
		if len(text) == 0 {
			continue
		}
		if len(content) > 0 {
			content = append(content, '\n', '\n')
			owners = append(owners, index, index)
		}
		content = append(content, text...)
		for range text {
			owners = append(owners, index)
		}
	}
	if len(content) == 0 {
		return nil, permanent("EMPTY_DOCUMENT", "document contains no extractable text", nil)
	}
	chunks := make([]Chunk, 0, (len(content)+chunkSize-1)/chunkSize)
	for start := 0; start < len(content); {
		for start < len(content) && unicode.IsSpace(content[start]) {
			start++
		}
		if start >= len(content) {
			break
		}
		end := start + chunkSize
		if end > len(content) {
			end = len(content)
		} else {
			end = preferredBoundary(content, start, end)
		}
		trimmedEnd := end
		for trimmedEnd > start && unicode.IsSpace(content[trimmedEnd-1]) {
			trimmedEnd--
		}
		if trimmedEnd == start {
			trimmedEnd = end
		}
		text := string(content[start:trimmedEnd])
		firstOwner, lastOwner := owners[start], owners[trimmedEnd-1]
		chunk := metadataForBlocks(blocks, firstOwner, lastOwner)
		chunk.Index = len(chunks)
		chunk.Content = text
		chunk.TokenCount = estimateTokens(text)
		chunk.ContentHash = fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
		chunks = append(chunks, chunk)
		if end >= len(content) {
			break
		}
		next := end - overlap
		if next <= start {
			next = end
		}
		for next < end && unicode.IsSpace(content[next]) {
			next++
		}
		start = next
	}
	return chunks, nil
}

var chunkBoundaries = [][]rune{
	[]rune("\n\n"), []rune("\n"), []rune("。"), []rune("！"), []rune("？"),
	[]rune(". "), []rune("! "), []rune("? "), []rune("；"), []rune("; "),
	[]rune("，"), []rune(", "), []rune(" "),
}

func preferredBoundary(content []rune, start, maximum int) int {
	minimum := start + (maximum-start)/2
	for _, boundary := range chunkBoundaries {
		for index := maximum - len(boundary); index >= minimum; index-- {
			if runesEqual(content[index:index+len(boundary)], boundary) {
				return index + len(boundary)
			}
		}
	}
	return maximum
}

func runesEqual(left, right []rune) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func metadataForBlocks(blocks []SourceBlock, first, last int) Chunk {
	chunk := Chunk{Metadata: map[string]any{"source_block_start": first, "source_block_end": last}}
	var paragraphStart, paragraphEnd *int
	for index := first; index <= last && index < len(blocks); index++ {
		block := blocks[index]
		chunk.PageStart = minimumPointer(chunk.PageStart, block.PageStart)
		chunk.PageEnd = maximumPointer(chunk.PageEnd, block.PageEnd)
		paragraphStart = minimumPointer(paragraphStart, block.ParagraphFrom)
		paragraphEnd = maximumPointer(paragraphEnd, block.ParagraphTo)
		if chunk.HeadingPath == "" && block.HeadingPath != "" {
			chunk.HeadingPath = block.HeadingPath
		}
	}
	if chunk.PageStart != nil {
		chunk.Metadata["page_start"], chunk.Metadata["page_end"] = *chunk.PageStart, *chunk.PageEnd
	}
	if paragraphStart != nil {
		chunk.Metadata["paragraph_start"], chunk.Metadata["paragraph_end"] = *paragraphStart, *paragraphEnd
	}
	return chunk
}

func minimumPointer(current, candidate *int) *int {
	if candidate == nil {
		return current
	}
	if current == nil || *candidate < *current {
		value := *candidate
		return &value
	}
	return current
}

func maximumPointer(current, candidate *int) *int {
	if candidate == nil {
		return current
	}
	if current == nil || *candidate > *current {
		value := *candidate
		return &value
	}
	return current
}

func estimateTokens(text string) int {
	count := (utf8.RuneCountInString(text) + 3) / 4
	if count < 1 {
		return 1
	}
	return count
}
