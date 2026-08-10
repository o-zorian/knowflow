package ingestion

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRecursiveChunkerHonorsBoundariesAndOverlap(t *testing.T) {
	blocks := []SourceBlock{{Content: strings.Repeat("alpha ", 30), ParagraphFrom: intPtr(1), ParagraphTo: intPtr(1)},
		{Content: strings.Repeat("beta ", 30), ParagraphFrom: intPtr(2), ParagraphTo: intPtr(2)}}
	chunks, err := (RecursiveChunker{}).Chunk(blocks, 80, 15)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 3 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for index, chunk := range chunks {
		if utf8.RuneCountInString(chunk.Content) > 80 || chunk.Index != index || len(chunk.ContentHash) != 64 {
			t.Fatalf("invalid chunk %d: %+v", index, chunk)
		}
	}
	for index := 1; index < len(chunks); index++ {
		previous := []rune(chunks[index-1].Content)
		current := []rune(chunks[index].Content)
		matched := false
		for overlap := 1; overlap <= 15 && overlap <= len(previous) && overlap <= len(current); overlap++ {
			if string(previous[len(previous)-overlap:]) == string(current[:overlap]) {
				matched = true
			}
		}
		if !matched {
			t.Fatalf("chunks %d and %d have no overlap", index-1, index)
		}
	}
}

func TestChunkMetadataSpansPagesAndParagraphs(t *testing.T) {
	blocks := []SourceBlock{
		{Content: "first", PageStart: intPtr(1), PageEnd: intPtr(1), HeadingPath: "One"},
		{Content: "second", PageStart: intPtr(2), PageEnd: intPtr(2), HeadingPath: "Two"},
	}
	chunks, err := (RecursiveChunker{}).Chunk(blocks, 100, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].PageStart == nil || *chunks[0].PageStart != 1 || *chunks[0].PageEnd != 2 || chunks[0].HeadingPath != "One" {
		t.Fatalf("metadata was not preserved: %+v", chunks)
	}
}
