package document

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"knowflow/internal/apperror"
)

type preparedUpload struct {
	path     string
	filename string
	mimeType string
	size     int64
	sha256   string
}

func prepareUpload(reader io.Reader, filename, declaredMIME string, maxSize int64) (preparedUpload, error) {
	if err := validateFilename(filename); err != nil {
		return preparedUpload{}, err
	}
	extension := strings.ToLower(filepath.Ext(filename))
	canonicalMIME, ok := extensionMIME[extension]
	if !ok {
		return preparedUpload{}, apperror.New(http.StatusBadRequest, "UNSUPPORTED_FILE_EXTENSION", "supported file extensions are .pdf, .docx, .md, .markdown, and .txt")
	}
	if declaredMIME != "" {
		mediaType, _, err := mime.ParseMediaType(declaredMIME)
		if err != nil || !declaredAllowed(extension, strings.ToLower(mediaType)) {
			return preparedUpload{}, apperror.New(http.StatusBadRequest, "INVALID_FILE_MIME", "file MIME type does not match its extension")
		}
	}
	temporary, err := os.CreateTemp("", "knowflow-upload-*")
	if err != nil {
		return preparedUpload{}, apperror.Wrap(http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error", err)
	}
	path := temporary.Name()
	cleanup := func() { _ = temporary.Close(); _ = os.Remove(path) }
	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(temporary, hasher), io.LimitReader(reader, maxSize+1))
	if err != nil {
		cleanup()
		return preparedUpload{}, apperror.Wrap(http.StatusBadRequest, "UPLOAD_READ_FAILED", "file upload could not be read", err)
	}
	if size > maxSize {
		cleanup()
		return preparedUpload{}, apperror.New(http.StatusRequestEntityTooLarge, "FILE_TOO_LARGE", "file exceeds the configured upload size limit")
	}
	if size == 0 {
		cleanup()
		return preparedUpload{}, apperror.New(http.StatusBadRequest, "EMPTY_FILE", "file is empty")
	}
	if err := temporary.Close(); err != nil {
		cleanup()
		return preparedUpload{}, apperror.Wrap(http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error", err)
	}
	if err := validateContent(path, extension); err != nil {
		_ = os.Remove(path)
		return preparedUpload{}, err
	}
	return preparedUpload{path: path, filename: filename, mimeType: canonicalMIME, size: size, sha256: hex.EncodeToString(hasher.Sum(nil))}, nil
}

func validateFilename(filename string) error {
	if filename == "" || filename != strings.TrimSpace(filename) || utf8.RuneCountInString(filename) > 255 ||
		strings.ContainsAny(filename, "/\\") || filename == "." || filename == ".." || strings.ContainsRune(filename, '\x00') {
		return apperror.New(http.StatusBadRequest, "INVALID_FILENAME", "filename is invalid")
	}
	for _, r := range filename {
		if unicode.IsControl(r) {
			return apperror.New(http.StatusBadRequest, "INVALID_FILENAME", "filename is invalid")
		}
	}
	return nil
}

func validateContent(path, extension string) error {
	file, err := os.Open(path)
	if err != nil {
		return apperror.Wrap(http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error", err)
	}
	buffer := make([]byte, 512)
	n, readErr := file.Read(buffer)
	_ = file.Close()
	if readErr != nil && readErr != io.EOF {
		return apperror.Wrap(http.StatusBadRequest, "UPLOAD_READ_FAILED", "file upload could not be read", readErr)
	}
	detected := http.DetectContentType(buffer[:n])
	switch extension {
	case ".pdf":
		if detected != "application/pdf" {
			return apperror.New(http.StatusBadRequest, "INVALID_FILE_CONTENT", "file content does not match its extension")
		}
	case ".docx":
		if err := validateDOCX(path); err != nil {
			return apperror.New(http.StatusBadRequest, "INVALID_FILE_CONTENT", "file is not a valid DOCX document")
		}
	case ".md", ".markdown", ".txt":
		if !strings.HasPrefix(detected, "text/plain") && detected != "text/markdown; charset=utf-8" {
			return apperror.New(http.StatusBadRequest, "INVALID_FILE_CONTENT", "file content does not match its extension")
		}
	}
	return nil
}

func validateDOCX(path string) error {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer archive.Close()
	contentTypes, document := false, false
	for _, file := range archive.File {
		switch file.Name {
		case "[Content_Types].xml":
			contentTypes = true
		case "word/document.xml":
			document = true
		}
	}
	if !contentTypes || !document {
		return fmt.Errorf("required DOCX entries are missing")
	}
	return nil
}

func declaredAllowed(extension, mediaType string) bool {
	if mediaType == "application/octet-stream" {
		return true
	}
	switch extension {
	case ".pdf":
		return mediaType == "application/pdf"
	case ".docx":
		return mediaType == "application/vnd.openxmlformats-officedocument.wordprocessingml.document" || mediaType == "application/zip"
	case ".md", ".markdown":
		return mediaType == "text/markdown" || mediaType == "text/plain"
	case ".txt":
		return mediaType == "text/plain"
	default:
		return false
	}
}

var extensionMIME = map[string]string{
	".pdf":      "application/pdf",
	".docx":     "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".md":       "text/markdown",
	".markdown": "text/markdown",
	".txt":      "text/plain",
}
