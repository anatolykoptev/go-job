package websearch

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// yandexXMLResponse is the XML search result envelope.
type yandexXMLResponse struct {
	XMLName  xml.Name       `xml:"yandexsearch"`
	Response yandexRespBody `xml:"response"`
}

type yandexRespBody struct {
	Error   *yandexXMLError  `xml:"error"`
	Results yandexXMLResults `xml:"results"`
}

type yandexXMLError struct {
	Code    int    `xml:"code,attr"`
	Message string `xml:",chardata"`
}

type yandexXMLResults struct {
	Grouping yandexXMLGrouping `xml:"grouping"`
}

type yandexXMLGrouping struct {
	Groups []yandexXMLGroup `xml:"group"`
}

type yandexXMLGroup struct {
	Docs []yandexXMLDoc `xml:"doc"`
}

type yandexXMLDoc struct {
	URL      string            `xml:"url"`
	Domain   string            `xml:"domain"`
	Title    string            `xml:"title"`
	Headline string            `xml:"headline"`
	Passages yandexXMLPassages `xml:"passages"`
}

type yandexXMLPassages struct {
	Passage []string `xml:"passage"`
}

// ParseYandexXML extracts search results from Yandex Search API XML response.
func ParseYandexXML(data []byte) ([]Result, error) {
	var resp yandexXMLResponse
	if err := xml.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("xml unmarshal: %w", err)
	}

	if resp.Response.Error != nil {
		return nil, fmt.Errorf("yandex error %d: %s",
			resp.Response.Error.Code, resp.Response.Error.Message)
	}

	var results []Result
	for _, group := range resp.Response.Results.Grouping.Groups {
		for _, doc := range group.Docs {
			if doc.URL == "" {
				continue
			}

			title := cleanXMLText(doc.Title)
			if title == "" {
				title = doc.Headline
			}

			content := doc.Headline
			if len(doc.Passages.Passage) > 0 {
				content = strings.Join(doc.Passages.Passage, " ")
			}
			content = cleanXMLText(content)

			results = append(results, Result{
				Title:    title,
				Content:  content,
				URL:      doc.URL,
				Score:    directResultScore,
				Metadata: map[string]string{"engine": "yandex"},
			})
		}
	}

	return results, nil
}

// cleanXMLText strips Yandex highlight tags from text.
func cleanXMLText(s string) string {
	s = strings.ReplaceAll(s, "<hlword>", "")
	s = strings.ReplaceAll(s, "</hlword>", "")
	return strings.TrimSpace(s)
}
