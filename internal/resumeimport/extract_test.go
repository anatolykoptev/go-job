package resumeimport

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

func buildDocx(t *testing.T, parts map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, xmlBody := range parts {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(xmlBody)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const docxBody = `<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` +
	`<w:p><w:r><w:t>Jane Doe</w:t></w:r></w:p>` +
	`<w:p><w:r><w:t>Go</w:t></w:r><w:r><w:tab/></w:r><w:r><w:t>Engineer</w:t></w:r></w:p>` +
	`<w:p><w:r><w:t>Line one</w:t></w:r><w:r><w:br/></w:r><w:r><w:t>Line two</w:t></w:r></w:p>` +
	`</w:body></w:document>`

func TestExtract_Text(t *testing.T) {
	got, err := Extract([]byte("\ufeffName\r\nRole\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "Name\nRole" {
		t.Fatalf("got %q", got)
	}
}

func TestExtract_Docx(t *testing.T) {
	data := buildDocx(t, map[string]string{
		"word/document.xml":   docxBody,
		"word/header1.xml":    `<?xml version="1.0"?><w:hdr xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:p><w:r><w:t>Confidential</w:t></w:r></w:p></w:hdr>`,
		"[Content_Types].xml": `<?xml version="1.0"?><Types/>`,
	})
	got, err := Extract(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Jane Doe", "Go", "Engineer", "Line one\nLine two", "Confidential"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}

func TestExtract_PKNotDocx(t *testing.T) {
	data := buildDocx(t, map[string]string{"some/random.bin": "not xml"})
	if _, err := Extract(data); !errors.Is(err, ErrEmptyText) {
		t.Fatalf("want ErrEmptyText, got %v", err)
	}
}

func TestExtract_PDF(t *testing.T) {
	data, err := os.ReadFile("testdata/minimal.pdf")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Extract(data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Jane Doe") {
		t.Fatalf("got %q", got)
	}
}

func TestExtract_BinaryGarbage(t *testing.T) {
	if _, err := Extract([]byte{0x89, 0x50, 0x4E, 0x47, 0xFF, 0x00}); !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("want ErrUnsupportedType, got %v", err)
	}
}

// Empty/whitespace-only text files must fail like the binary paths do —
// a "" extract downstream would rebuild an empty profile over a real one.
func TestExtract_EmptyTextFile(t *testing.T) {
	for _, data := range [][]byte{[]byte(""), []byte("   \n\t  \n")} {
		if _, err := Extract(data); !errors.Is(err, ErrEmptyText) {
			t.Fatalf("input %q: want ErrEmptyText, got %v", data, err)
		}
	}
}

// A deflate bomb — 8 MB of text in a few KB of zip — must be bounded by the
// extraction cap, not inflated into gigabytes.
func TestExtract_DocxDecompressionBound(t *testing.T) {
	big := strings.Repeat("x", 8<<20)
	data := buildDocx(t, map[string]string{
		"word/document.xml": `<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>` + big + `</w:t></w:r></w:p></w:body></w:document>`,
	})
	if len(data) > 1<<20 {
		t.Fatalf("test artifact should compress small, got %d", len(data))
	}
	got, err := Extract(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > maxExtractedBytes+64 {
		t.Fatalf("extracted %d bytes — cap breached", len(got))
	}
}
