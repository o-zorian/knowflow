package ingestion

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/ledongthuc/pdf"
)

type SourceBlock struct {
	Content       string
	PageStart     *int
	PageEnd       *int
	ParagraphFrom *int
	ParagraphTo   *int
	HeadingPath   string
	Metadata      map[string]any
}

type Parser interface {
	Parse(ctx context.Context, filename, mimeType string, reader io.Reader) ([]SourceBlock, error)
}

type DocumentParser struct{}

func (DocumentParser) Parse(ctx context.Context, filename, _ string, reader io.Reader) (blocks []SourceBlock, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			blocks = nil
			err = permanent("DOCUMENT_PARSE_FAILED", "document could not be parsed", fmt.Errorf("parser panic: %v", recovered))
		}
	}()
	extension := strings.ToLower(filepath.Ext(filename))
	switch extension {
	case ".txt":
		blocks, err = parseText(ctx, reader)
	case ".md", ".markdown":
		blocks, err = parseMarkdown(ctx, reader)
	case ".pdf":
		blocks, err = parsePDF(ctx, reader)
	case ".docx":
		blocks, err = parseDOCX(ctx, reader)
	default:
		return nil, permanent("UNSUPPORTED_DOCUMENT_TYPE", "document type is not supported", nil)
	}
	if err != nil {
		return nil, err
	}
	if len(blocks) == 0 {
		return nil, permanent("EMPTY_DOCUMENT", "document contains no extractable text", nil)
	}
	return blocks, nil
}

func parseText(ctx context.Context, reader io.Reader) ([]SourceBlock, error) {
	data, err := readAll(ctx, reader)
	if err != nil {
		return nil, transient("DOCUMENT_READ_FAILED", "document could not be read", err)
	}
	paragraphs := splitParagraphs(cleanText(string(data)))
	blocks := make([]SourceBlock, 0, len(paragraphs))
	for i, paragraph := range paragraphs {
		position := i + 1
		blocks = append(blocks, SourceBlock{Content: paragraph, ParagraphFrom: intPtr(position), ParagraphTo: intPtr(position), Metadata: map[string]any{"paragraph_start": position, "paragraph_end": position}})
	}
	return blocks, nil
}

func parseMarkdown(ctx context.Context, reader io.Reader) ([]SourceBlock, error) {
	data, err := readAll(ctx, reader)
	if err != nil {
		return nil, transient("DOCUMENT_READ_FAILED", "document could not be read", err)
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	headings := make([]string, 0, 6)
	blocks := make([]SourceBlock, 0)
	paragraph := make([]string, 0)
	paragraphIndex := 0
	flush := func() {
		content := cleanText(strings.Join(paragraph, "\n"))
		paragraph = paragraph[:0]
		if content == "" {
			return
		}
		paragraphIndex++
		path := strings.Join(headings, " > ")
		blocks = append(blocks, SourceBlock{Content: content, ParagraphFrom: intPtr(paragraphIndex), ParagraphTo: intPtr(paragraphIndex), HeadingPath: path,
			Metadata: map[string]any{"paragraph_start": paragraphIndex, "paragraph_end": paragraphIndex, "source_type": "markdown"}})
	}
	for _, raw := range lines {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line := strings.TrimSpace(raw)
		level, title := markdownHeading(line)
		if level > 0 {
			flush()
			for len(headings) < level {
				headings = append(headings, "")
			}
			headings = append(headings[:level-1], title)
			path := strings.Join(nonEmpty(headings), " > ")
			paragraphIndex++
			blocks = append(blocks, SourceBlock{Content: title, ParagraphFrom: intPtr(paragraphIndex), ParagraphTo: intPtr(paragraphIndex), HeadingPath: path,
				Metadata: map[string]any{"paragraph_start": paragraphIndex, "paragraph_end": paragraphIndex, "heading_level": level, "source_type": "markdown"}})
			continue
		}
		if line == "" {
			flush()
			continue
		}
		paragraph = append(paragraph, raw)
	}
	flush()
	return blocks, nil
}

func parsePDF(ctx context.Context, reader io.Reader) ([]SourceBlock, error) {
	temporary, err := os.CreateTemp("", "knowflow-parse-*.pdf")
	if err != nil {
		return nil, transient("DOCUMENT_PARSE_FAILED", "PDF document could not be parsed", err)
	}
	path := temporary.Name()
	defer os.Remove(path)
	if _, err := copyWithContext(ctx, temporary, reader); err != nil {
		_ = temporary.Close()
		return nil, transient("DOCUMENT_READ_FAILED", "document could not be read", err)
	}
	if err := temporary.Close(); err != nil {
		return nil, transient("DOCUMENT_READ_FAILED", "document could not be read", err)
	}
	file, pdfReader, err := pdf.Open(path)
	if err != nil {
		return nil, permanent("DOCUMENT_PARSE_FAILED", "PDF document could not be parsed", err)
	}
	defer file.Close()
	blocks := make([]SourceBlock, 0, pdfReader.NumPage())
	for pageNumber := 1; pageNumber <= pdfReader.NumPage(); pageNumber++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		content, err := pdfReader.Page(pageNumber).GetPlainText(nil)
		if err != nil {
			return nil, permanent("DOCUMENT_PARSE_FAILED", "PDF document could not be parsed", err)
		}
		content = cleanText(content)
		if content == "" {
			continue
		}
		page := pageNumber
		blocks = append(blocks, SourceBlock{Content: content, PageStart: &page, PageEnd: &page, Metadata: map[string]any{"page_start": page, "page_end": page, "source_type": "pdf"}})
	}
	return blocks, nil
}

func parseDOCX(ctx context.Context, reader io.Reader) ([]SourceBlock, error) {
	data, err := readAll(ctx, reader)
	if err != nil {
		return nil, transient("DOCUMENT_READ_FAILED", "document could not be read", err)
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, permanent("DOCUMENT_PARSE_FAILED", "DOCX document could not be parsed", err)
	}
	var documentXML io.ReadCloser
	for _, entry := range archive.File {
		if entry.Name == "word/document.xml" {
			documentXML, err = entry.Open()
			break
		}
	}
	if err != nil || documentXML == nil {
		return nil, permanent("DOCUMENT_PARSE_FAILED", "DOCX document could not be parsed", err)
	}
	defer documentXML.Close()
	return decodeDOCX(ctx, documentXML)
}

func decodeDOCX(ctx context.Context, reader io.Reader) ([]SourceBlock, error) {
	decoder := xml.NewDecoder(reader)
	blocks := make([]SourceBlock, 0)
	headings := make([]string, 0, 6)
	var paragraph, table strings.Builder
	paragraphIndex, tableDepth, paragraphDepth := 0, 0, 0
	paragraphStyle := ""
	inText := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, permanent("DOCUMENT_PARSE_FAILED", "DOCX document could not be parsed", err)
		}
		switch value := token.(type) {
		case xml.StartElement:
			switch value.Name.Local {
			case "tbl":
				tableDepth++
			case "p":
				paragraphDepth++
				paragraph.Reset()
				paragraphStyle = ""
			case "pStyle":
				for _, attribute := range value.Attr {
					if attribute.Name.Local == "val" {
						paragraphStyle = attribute.Value
					}
				}
			case "t":
				inText = true
			case "tab", "br":
				if tableDepth > 0 {
					table.WriteByte(' ')
				} else if paragraphDepth > 0 {
					paragraph.WriteByte(' ')
				}
			}
		case xml.CharData:
			if inText {
				if tableDepth > 0 {
					table.Write([]byte(value))
				} else if paragraphDepth > 0 {
					paragraph.Write([]byte(value))
				}
			}
		case xml.EndElement:
			switch value.Name.Local {
			case "t":
				inText = false
			case "tc":
				if tableDepth > 0 {
					table.WriteByte('\t')
				}
			case "tr":
				if tableDepth > 0 {
					table.WriteByte('\n')
				}
			case "p":
				paragraphDepth--
				paragraphIndex++
				if tableDepth == 0 {
					content := cleanText(paragraph.String())
					if content != "" {
						level := headingLevel(paragraphStyle)
						if level > 0 {
							for len(headings) < level {
								headings = append(headings, "")
							}
							headings = append(headings[:level-1], content)
						}
						path := strings.Join(nonEmpty(headings), " > ")
						metadata := map[string]any{"paragraph_start": paragraphIndex, "paragraph_end": paragraphIndex, "source_type": "docx"}
						if level > 0 {
							metadata["heading_level"] = level
						}
						blocks = append(blocks, SourceBlock{Content: content, ParagraphFrom: intPtr(paragraphIndex), ParagraphTo: intPtr(paragraphIndex), HeadingPath: path, Metadata: metadata})
					}
				}
			case "tbl":
				tableDepth--
				if tableDepth == 0 {
					content := cleanText(table.String())
					table.Reset()
					if content != "" {
						path := strings.Join(nonEmpty(headings), " > ")
						blocks = append(blocks, SourceBlock{Content: content, ParagraphFrom: intPtr(paragraphIndex), ParagraphTo: intPtr(paragraphIndex), HeadingPath: path,
							Metadata: map[string]any{"paragraph_start": paragraphIndex, "paragraph_end": paragraphIndex, "source_type": "docx", "content_type": "table"}})
					}
				}
			}
		}
	}
	return blocks, nil
}

func readAll(ctx context.Context, reader io.Reader) ([]byte, error) {
	var buffer bytes.Buffer
	_, err := copyWithContext(ctx, &buffer, reader)
	return buffer.Bytes(), err
}

func copyWithContext(ctx context.Context, writer io.Writer, reader io.Reader) (int64, error) {
	return io.Copy(writer, &contextReader{ctx: ctx, reader: reader})
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

func cleanText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	var cleaned strings.Builder
	for _, r := range text {
		if (unicode.IsControl(r) || unicode.In(r, unicode.Cf)) && r != '\n' && r != '\t' {
			continue
		}
		cleaned.WriteRune(r)
	}
	lines := strings.Split(cleaned.String(), "\n")
	result := make([]string, 0, len(lines))
	blank := true
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			if !blank {
				result = append(result, "")
			}
			blank = true
			continue
		}
		result = append(result, line)
		blank = false
	}
	return strings.TrimSpace(strings.Join(result, "\n"))
}

func splitParagraphs(text string) []string {
	if text == "" {
		return nil
	}
	parts := strings.Split(text, "\n\n")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

func markdownHeading(line string) (int, string) {
	count := 0
	for count < len(line) && count < 6 && line[count] == '#' {
		count++
	}
	if count == 0 || len(line) <= count || line[count] != ' ' {
		return 0, ""
	}
	return count, strings.TrimSpace(line[count+1:])
}

func headingLevel(style string) int {
	style = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(style), " ", ""))
	style = strings.TrimPrefix(style, "heading")
	level, err := strconv.Atoi(style)
	if err == nil && level >= 1 && level <= 6 {
		return level
	}
	if style == "title" {
		return 1
	}
	return 0
}

func nonEmpty(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func intPtr(value int) *int { return &value }

func parseError(err error) error {
	if err == nil {
		return nil
	}
	var processing *ProcessingError
	if errors.As(err, &processing) {
		return processing
	}
	return permanent("DOCUMENT_PARSE_FAILED", "document could not be parsed", fmt.Errorf("parse document: %w", err))
}
