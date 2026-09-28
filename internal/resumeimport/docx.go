package resumeimport

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// maxDocxPartInflated backstops per-part decompression: deflate expands ~1000x,
// so the xml decoder reads a bounded stream. Text accumulation stops cleanly at
// maxExtractedBytes — truncation, not an error.
const maxDocxPartInflated = 8 << 20

// extractDOCX pulls text out of a .docx (an OOXML zip): word/document.xml plus
// any word/header*.xml / word/footer*.xml parts (names often live in headers).
// <w:t> carries runs of text, </w:p> breaks lines, <w:br/> and <w:tab/> are
// inline breaks/tabs. Stdlib only — no dependency.
func extractDOCX(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("docx: %w", err)
	}
	var sb strings.Builder
	var found bool
	parts := 0
	for _, f := range zr.File {
		if !isDocxTextPart(f.Name) {
			continue
		}
		parts++
		if parts > maxDocxParts {
			break
		}
		if err := docxPartText(f, &sb); err != nil {
			return "", fmt.Errorf("docx %s: %w", f.Name, err)
		}
		sb.WriteString("\n")
		found = true
		if sb.Len() >= maxExtractedBytes {
			break
		}
	}
	if !found || strings.TrimSpace(sb.String()) == "" {
		return "", ErrEmptyText
	}
	return Normalize(sb.String()), nil
}

func isDocxTextPart(name string) bool {
	if name == "word/document.xml" {
		return true
	}
	return strings.HasSuffix(name, ".xml") &&
		(strings.HasPrefix(name, "word/header") || strings.HasPrefix(name, "word/footer"))
}

// docxPartText appends the part's text to sb, stopping early once the global
// output budget is spent. A decode error after some text survives yields the
// partial text rather than failing the whole upload.
func docxPartText(f *zip.File, sb *strings.Builder) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	dec := xml.NewDecoder(io.LimitReader(rc, maxDocxPartInflated))
	inText := false
	for {
		if sb.Len() >= maxExtractedBytes {
			return nil
		}
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			// The inflate cap truncating mid-element lands here — keep the
			// partial text, it is still a usable resume.
			if sb.Len() > 0 {
				return nil
			}
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "br", "cr":
				sb.WriteString("\n")
			case "tab":
				sb.WriteString("\t")
			}
		case xml.CharData:
			if inText {
				s := string(t)
				if budget := maxExtractedBytes - sb.Len(); budget < len(s) {
					if budget < 0 {
						budget = 0
					}
					s = s[:budget]
				}
				sb.WriteString(s)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p", "tr":
				sb.WriteString("\n")
			}
		}
	}
}
