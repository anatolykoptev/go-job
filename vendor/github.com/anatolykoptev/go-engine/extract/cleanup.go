package extract

import (
	"bytes"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// StripScriptStyle removes <script> and <style> elements from raw HTML
// using the tokenizer (no full DOM parse). Returns cleaned HTML bytes.
func StripScriptStyle(body []byte) []byte {
	if len(body) == 0 {
		return body
	}

	var buf bytes.Buffer
	buf.Grow(len(body))
	z := html.NewTokenizer(bytes.NewReader(body))

	skip := false
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}

		tn, _ := z.TagName()
		tag := atom.Lookup(tn)

		switch tt {
		case html.StartTagToken:
			if tag == atom.Script || tag == atom.Style {
				skip = true
				continue
			}
		case html.EndTagToken:
			if tag == atom.Script || tag == atom.Style {
				skip = false
				continue
			}
		}

		if !skip {
			buf.Write(z.Raw())
		}
	}

	return buf.Bytes()
}
