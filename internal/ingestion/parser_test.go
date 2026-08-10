package ingestion

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestDocumentParserSupportsTextAndMarkdownHierarchy(t *testing.T) {
	parser := DocumentParser{}
	textBlocks, err := parser.Parse(context.Background(), "notes.txt", "text/plain", strings.NewReader(" first\t line\x00\n\n\nsecond   line "))
	if err != nil {
		t.Fatal(err)
	}
	if len(textBlocks) != 2 || textBlocks[0].Content != "first line" || *textBlocks[1].ParagraphFrom != 2 {
		t.Fatalf("unexpected TXT blocks: %+v", textBlocks)
	}
	markdown := "# Product\n\nOverview text.\n\n## API\n\nEndpoint details."
	markdownBlocks, err := parser.Parse(context.Background(), "notes.md", "text/markdown", strings.NewReader(markdown))
	if err != nil {
		t.Fatal(err)
	}
	if len(markdownBlocks) != 4 || markdownBlocks[3].HeadingPath != "Product > API" || markdownBlocks[2].Metadata["heading_level"] != 2 {
		t.Fatalf("markdown hierarchy was not preserved: %+v", markdownBlocks)
	}
}

func TestDocumentParserSupportsDOCXParagraphsHeadingsAndTables(t *testing.T) {
	var document bytes.Buffer
	archive := zip.NewWriter(&document)
	entry, err := archive.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	xml := `<w:document xmlns:w="urn:test"><w:body>
		<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>Title</w:t></w:r></w:p>
		<w:p><w:r><w:t>Body text</w:t></w:r></w:p>
		<w:tbl><w:tr><w:tc><w:p><w:r><w:t>A1</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>B1</w:t></w:r></w:p></w:tc></w:tr></w:tbl>
	</w:body></w:document>`
	_, _ = entry.Write([]byte(xml))
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	blocks, err := (DocumentParser{}).Parse(context.Background(), "report.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", bytes.NewReader(document.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 3 || blocks[1].HeadingPath != "Title" || blocks[2].Metadata["content_type"] != "table" || !strings.Contains(blocks[2].Content, "A1") {
		t.Fatalf("unexpected DOCX blocks: %+v", blocks)
	}
}

func TestDocumentParserSupportsPDFPageMetadata(t *testing.T) {
	blocks, err := (DocumentParser{}).Parse(context.Background(), "sample.pdf", "application/pdf", bytes.NewReader(minimalPDF("Hello PDF")))
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].PageStart == nil || *blocks[0].PageStart != 1 || !strings.Contains(blocks[0].Content, "Hello PDF") {
		t.Fatalf("unexpected PDF blocks: %+v", blocks)
	}
}

func TestDocumentParserRejectsEmptyExtractedContent(t *testing.T) {
	_, err := (DocumentParser{}).Parse(context.Background(), "empty.txt", "text/plain", strings.NewReader("\x00\n\t"))
	processing := classify(err, "fallback", "fallback", true)
	if err == nil || processing.Code != "EMPTY_DOCUMENT" || processing.Retryable {
		t.Fatalf("unexpected empty document error: %#v", err)
	}
}

func minimalPDF(text string) []byte {
	var buffer bytes.Buffer
	buffer.WriteString("%PDF-1.4\n")
	offsets := make([]int, 6)
	writeObject := func(number int, body string) {
		offsets[number] = buffer.Len()
		fmt.Fprintf(&buffer, "%d 0 obj\n%s\nendobj\n", number, body)
	}
	writeObject(1, "<< /Type /Catalog /Pages 2 0 R >>")
	writeObject(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	writeObject(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>")
	stream := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", text)
	writeObject(4, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	writeObject(5, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	xref := buffer.Len()
	buffer.WriteString("xref\n0 6\n0000000000 65535 f \n")
	for number := 1; number <= 5; number++ {
		fmt.Fprintf(&buffer, "%010d 00000 n \n", offsets[number])
	}
	fmt.Fprintf(&buffer, "trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xref)
	return buffer.Bytes()
}
