// Package fileopt provides byte-level optimization for PDFs and images by
// shelling out to system binaries (gs, cwebp, oxipng). Missing binaries are
// treated as "no-op" — the original bytes are returned with a warn log — so
// callers can enable optimization unconditionally and not worry about host
// setup.
package fileopt

import (
	"context"
	"strings"
)

// Level selects a ghostscript PDFSETTINGS preset. Valid values map 1:1 onto
// gs -dPDFSETTINGS flags. LevelEbook is a good default (150dpi, ~50-70% size
// reduction for typical text+image PDFs).
type Level string

const (
	LevelScreen   Level = "screen"   // 72dpi — smallest, readable but blurry
	LevelEbook    Level = "ebook"    // 150dpi — balanced default
	LevelPrinter  Level = "printer"  // 300dpi — higher quality
	LevelPrepress Level = "prepress" // 300dpi + color-preserving
)

// IsValid reports whether l is one of the recognized levels.
func (l Level) IsValid() bool {
	switch l {
	case LevelScreen, LevelEbook, LevelPrinter, LevelPrepress:
		return true
	}
	return false
}

// Kind selects the optimizer pipeline. The zero value is KindUnsupported;
// safe-by-default for zero-valued struct fields (a caller who forgets to set
// Kind gets a passthrough, not a silent PDF optimization).
type Kind int

const (
	KindUnsupported Kind = iota
	KindPDF
	KindPNG
	KindWebP
)

// KindFromExt maps a file extension (with or without the leading dot, any case)
// to a Kind. Returns KindUnsupported for everything not handled yet (jpeg, gif).
func KindFromExt(ext string) Kind {
	switch strings.ToLower(strings.TrimPrefix(ext, ".")) {
	case "pdf":
		return KindPDF
	case "png":
		return KindPNG
	case "webp":
		return KindWebP
	default:
		return KindUnsupported
	}
}

// OptimizeBytes dispatches to the correct optimizer based on kind. Passes
// through unchanged for KindUnsupported. PDF uses level; WebP uses quality;
// PNG ignores both.
func OptimizeBytes(ctx context.Context, data []byte, kind Kind, level Level, quality int) ([]byte, error) {
	switch kind {
	case KindPDF:
		return OptimizePDF(ctx, data, level)
	case KindPNG:
		return OptimizePNG(ctx, data)
	case KindWebP:
		return OptimizeWebP(ctx, data, quality)
	default:
		return data, nil
	}
}

// Ratio returns optimized/original as a fraction. Values < 1.0 indicate size
// reduction; values > 1.0 indicate growth (unusual but possible for already-
// compressed input or tiny fixtures where per-file overhead dominates).
// Returns 0 when originalBytes is 0 (guards div-by-zero).
func Ratio(originalBytes, optimizedBytes int) float64 {
	if originalBytes == 0 {
		return 0
	}
	return float64(optimizedBytes) / float64(originalBytes)
}
