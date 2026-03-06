package websearch

import "encoding/json"

// yandexRequest is the JSON body for searchAsync.
type yandexRequest struct {
	Query     yandexQuery     `json:"query"`
	SortSpec  yandexSortSpec  `json:"sortSpec"`
	GroupSpec yandexGroupSpec `json:"groupSpec"`
	MaxPass   string          `json:"maxPassages"`
	Region    string          `json:"region"`
	L10N      string          `json:"l10N"`
	FolderID  string          `json:"folderId"`
	Page      string          `json:"page"`
}

type yandexQuery struct {
	SearchType string `json:"searchType"`
	QueryText  string `json:"queryText"`
	FamilyMode string `json:"familyMode"`
}

type yandexSortSpec struct {
	SortMode  string `json:"sortMode"`
	SortOrder string `json:"sortOrder"`
}

type yandexGroupSpec struct {
	GroupMode    string `json:"groupMode"`
	GroupsOnPage string `json:"groupsOnPage"`
	DocsInGroup  string `json:"docsInGroup"`
}

// yandexOperation is the async operation response.
type yandexOperation struct {
	ID       string          `json:"id"`
	Done     bool            `json:"done"`
	Response json.RawMessage `json:"response"`
	Error    *yandexError    `json:"error"`
}

type yandexError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
