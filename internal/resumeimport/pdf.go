package resumeimport

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/ledongthuc/pdf"
)

// extractPDF via ledongthuc/pdf (pure Go, MIT). Image-only (scanned) PDFs yield
// no text layer — reported as ErrScannedPDF so the UI can ask for pasted text.
//
// The library materializes each page's text fully, so text is accumulated
// page-by-page under maxExtractedBytes — that bounds multi-page inflation; a
// single malicious page can still decompress large inside the parser, which
// the recover surfaces as a clean error rather than a dropped connection.
func extractPDF(data []byte) (text string, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			text, err = "", fmt.Errorf("pdf: %v", rec)
		}
	}()
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("pdf: %w", err)
	}
	pages := r.NumPage()
	if pages > maxPDFPages {
		pages = maxPDFPages
	}
	var sb strings.Builder
	for i := 1; i <= pages; i++ {
		t, perr := r.Page(i).GetPlainText(nil)
		if perr != nil {
			if sb.Len() > 0 {
				break
			}
			return "", fmt.Errorf("pdf: %w", perr)
		}
		sb.WriteString(t)
		if sb.Len() >= maxExtractedBytes {
			break
		}
	}
	if s := Normalize(sb.String()); s != "" {
		return s, nil
	}
	return "", ErrScannedPDF
}
