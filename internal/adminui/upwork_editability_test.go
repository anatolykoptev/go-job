package adminui

import (
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
)

// TestParseOrderedIDs_RepeatedIDField verifies that repeated "id" form fields
// are parsed into a slice of ints in order.
// Red-on-revert: remove parseOrderedIDs or change param name → nil slice.
func TestParseOrderedIDs_RepeatedIDField(t *testing.T) {
	form := url.Values{
		"id": {"3", "1", "2"},
	}
	r := &http.Request{
		Form: form,
	}
	ids, err := parseOrderedIDs(r)
	if err != nil {
		t.Fatalf("parseOrderedIDs: %v", err)
	}
	if len(ids) != 3 || ids[0] != 3 || ids[1] != 1 || ids[2] != 2 {
		t.Errorf("ids: got %v want [3 1 2]", ids)
	}
}

// TestParseOrderedIDs_CommaSep verifies the "order" fallback for drag-drop JS output.
func TestParseOrderedIDs_CommaSep(t *testing.T) {
	form := url.Values{
		"order": {"5,2,8"},
	}
	r := &http.Request{Form: form}
	ids, err := parseOrderedIDs(r)
	if err != nil {
		t.Fatalf("parseOrderedIDs: %v", err)
	}
	if len(ids) != 3 || ids[0] != 5 || ids[1] != 2 || ids[2] != 8 {
		t.Errorf("ids: got %v want [5 2 8]", ids)
	}
}

// TestParseOrderedIDs_InvalidReturnsError verifies non-integer values return an error.
func TestParseOrderedIDs_InvalidReturnsError(t *testing.T) {
	form := url.Values{
		"id": {"1", "abc", "3"},
	}
	r := &http.Request{Form: form}
	_, err := parseOrderedIDs(r)
	if err == nil {
		t.Error("expected error for non-integer id, got nil")
	}
}

// TestUpworkTmpl_DraggableMarkup verifies that the upworkTmplSrc template
// contains the required draggable markup for P4b drag-drop JS integration.
// Red-on-revert: remove gd-sortable or gd-sortable-item → test fails.
func TestUpworkTmpl_DraggableMarkup(t *testing.T) {
	src := upworkTmplSrc

	for _, want := range []string{
		`class="gd-sortable"`,
		`data-reorder-url=`,
		`data-csrf=`,
		`class="gd-sortable-item`,
		`draggable="true"`,
		`data-id=`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("upworkTmplSrc missing draggable markup: %q", want)
		}
	}
}

// TestUpworkTmpl_CatalogSection verifies the catalog section exists in the template.
// Red-on-revert: remove catalog section → test fails.
func TestUpworkTmpl_CatalogSection(t *testing.T) {
	src := upworkTmplSrc

	for _, want := range []string{
		`/admin/upwork/catalog`,
		`/admin/upwork/catalog/reorder`,
		`UWCatalog`,
		`name="title"`,
		`name="description"`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("upworkTmplSrc missing catalog section element: %q", want)
		}
	}
}

// TestUpworkTmpl_CategoriesEditForm verifies the categories edit form exists.
// Red-on-revert: remove categories form → test fails.
func TestUpworkTmpl_CategoriesEditForm(t *testing.T) {
	src := upworkTmplSrc

	for _, want := range []string{
		`/admin/upwork/categories`,
		`name="category"`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("upworkTmplSrc missing categories edit form element: %q", want)
		}
	}
}

// TestUpworkFitness_NoPrintTemplateHTML checks that no template.HTML is used.
func TestUpworkFitness_NoPrintTemplateHTML(t *testing.T) {
	src, err := os.ReadFile("upwork.go")
	if err != nil {
		t.Fatalf("read upwork.go: %v", err)
	}
	if strings.Contains(string(src), "template.HTML") {
		t.Error("upwork.go must not use template.HTML (content must go through auto-escape)")
	}
}

// TestUpworkFitness_SkillReorderRoute verifies skill reorder route is present in adminui.go.
func TestUpworkFitness_SkillReorderRoute(t *testing.T) {
	src, err := os.ReadFile("adminui.go")
	if err != nil {
		t.Fatalf("read adminui.go: %v", err)
	}
	for _, want := range []string{
		"upwork/catalog",
		"upwork/skill/reorder",
		"upwork/categories",
	} {
		if !strings.Contains(string(src), want) {
			t.Errorf("adminui.go missing route for %q", want)
		}
	}
}
