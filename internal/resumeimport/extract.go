// Package resumeimport extracts plain text from uploaded resume files
// (txt/md/docx/pdf) so they can feed the master-resume build pipeline.
//
// Dispatch is on magic bytes, never on the filename: a .docx renamed to
// .pdf still routes to the zip extractor, and a random binary is refused
// by the UTF-8 check rather than producing garbage text.
package resumeimport

import (
	"bytes"
	"errors"
	"strings"
	"unicode/utf8"
)

// Sentinel errors the HTTP layer maps to user-facing messages.
var (
	ErrUnsupportedType = errors.New("unsupported file type")
	ErrScannedPDF      = errors.New("pdf contains no extractable text layer")
	ErrEmptyText       = errors.New("no text extracted")
)

const (
	// maxExtractedBytes bounds extracted text. The LLM parse window is 12k
	// runes, so anything past this is waste the seam would truncate anyway —
	// and the cap stops deflate bombs from inflating a 4 MB upload into
	// gigabytes of builder output (extraction runs before the rate limiter).
	maxExtractedBytes = 1 << 20
	// maxDocxParts caps zip entries scanned for text parts.
	maxDocxParts = 64
	// maxPDFPages caps pages walked by the PDF extractor.
	maxPDFPages = 64
)

// Extract returns the plain text of data.
func Extract(data []byte) (string, error) {
	switch {
	case bytes.HasPrefix(data, []byte("%PDF-")):
		return extractPDF(data)
	case bytes.HasPrefix(data, []byte("PK\x03\x04")):
		return extractDOCX(data)
	case utf8.Valid(data):
		if s := Normalize(string(data)); s != "" {
			return s, nil
		}
		return "", ErrEmptyText
	default:
		return "", ErrUnsupportedType
	}
}

// Normalize strips the BOM and converts CRLF/CR to LF.
func Normalize(s string) string {
	s = strings.TrimPrefix(s, "\uFEFF")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.TrimSpace(s)
}
