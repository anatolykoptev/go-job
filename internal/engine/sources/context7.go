package sources

import (
	"github.com/anatolykoptev/go_job/internal/engine"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

const context7BaseURL = "https://context7.com/api"

// --- Types ---

type c7SearchResponse struct {
	Results []C7Library `json:"results"`
}

// C7Library represents a library from Context7 search.
type C7Library struct {
	ID             string  `json:"id"`
	Title          string  `json:"title"`
	Description    string  `json:"description"`
	TotalSnippets  int     `json:"totalSnippets"`
	BenchmarkScore float64 `json:"benchmarkScore"`
	State          string  `json:"state"`
	Stars          int     `json:"stars"`
}

type c7ContextResponse struct {
	CodeSnippets []c7CodeSnippet `json:"codeSnippets"`
	InfoSnippets []c7InfoSnippet `json:"infoSnippets"`
}

type c7CodeSnippet struct {
	CodeTitle       string       `json:"codeTitle"`
	CodeDescription string       `json:"codeDescription"`
	CodeLanguage    string       `json:"codeLanguage"`
	CodeList        []c7CodeItem `json:"codeList"`
	PageTitle       string       `json:"pageTitle"`
}

type c7CodeItem struct {
	Language string `json:"language"`
	Code     string `json:"code"`
}

type c7InfoSnippet struct {
	Content    string `json:"content"`
	Breadcrumb string `json:"breadcrumb"`
}

// --- API calls ---

// resolveLibrary searches Context7 for a library by name.
func resolveLibrary(ctx context.Context, query, libraryName string) (*C7Library, error) {
	u := fmt.Sprintf("%s/v2/libs/search?%s", context7BaseURL, url.Values{
		"query":       {query},
		"libraryName": {libraryName},
	}.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	setC7Headers(req)

	resp, err := engine.Cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("context7 search status %d", resp.StatusCode)
	}

	var data c7SearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	// Return best finalized result
	for i := range data.Results {
		if data.Results[i].State == "finalized" && data.Results[i].TotalSnippets > 0 {
			return &data.Results[i], nil
		}
	}
	return nil, fmt.Errorf("no finalized library found for %q", libraryName)
}

// queryDocs fetches documentation from Context7 for a resolved library.
func queryDocs(ctx context.Context, query, libraryID string) (*c7ContextResponse, error) {
	u := fmt.Sprintf("%s/v2/context?%s", context7BaseURL, url.Values{
		"query":     {query},
		"libraryId": {libraryID},
		"type":      {"json"},
	}.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	setC7Headers(req)

	resp, err := engine.Cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("context7 docs status %d", resp.StatusCode)
	}

	var data c7ContextResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	return &data, nil
}

func setC7Headers(req *http.Request) {
	if engine.Cfg.Context7APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+engine.Cfg.Context7APIKey)
	}
	req.Header.Set("User-Agent", engine.UserAgentBot)
	req.Header.Set("X-Context7-Source", "go-search")
}

// --- Integration ---

// SearchContext7 resolves a library and fetches its docs, returning results
// as engine.SearxngResult slice for merging into the search pipeline.
func SearchContext7(ctx context.Context, query, libraryName string) ([]engine.SearxngResult, error) {
	lib, err := resolveLibrary(ctx, query, libraryName)
	if err != nil {
		return nil, fmt.Errorf("resolve: %w", err)
	}

	slog.Debug("context7: resolved library",
		slog.String("id", lib.ID),
		slog.String("title", lib.Title),
		slog.Int("snippets", lib.TotalSnippets),
	)

	docs, err := queryDocs(ctx, query, lib.ID)
	if err != nil {
		return nil, fmt.Errorf("query docs: %w", err)
	}

	var results []engine.SearxngResult

	// Convert code snippets to search results
	for _, cs := range docs.CodeSnippets {
		title := cs.CodeTitle
		if cs.PageTitle != "" {
			title = cs.PageTitle + " — " + cs.CodeTitle
		}

		var content strings.Builder
		if cs.CodeDescription != "" {
			content.WriteString(cs.CodeDescription)
			content.WriteString("\n\n")
		}
		for _, ci := range cs.CodeList {
			fmt.Fprintf(&content, "```%s\n%s\n```\n", ci.Language, ci.Code)
		}

		text := content.String()
		if len(text) > 2000 {
			text = text[:2000] + "..."
		}

		results = append(results, engine.SearxngResult{
			Title:   fmt.Sprintf("[Docs] %s — %s", lib.Title, title),
			URL:     "https://context7.com" + lib.ID,
			Content: text,
		})
	}

	// Convert info snippets to search results
	for _, is := range docs.InfoSnippets {
		title := lib.Title + " Documentation"
		if is.Breadcrumb != "" {
			title = is.Breadcrumb
		}

		text := is.Content
		if len(text) > 1500 {
			text = text[:1500] + "..."
		}

		results = append(results, engine.SearxngResult{
			Title:   "[Docs] " + title,
			URL:     "https://context7.com" + lib.ID,
			Content: text,
		})
	}

	// Cap total results to avoid overwhelming the pipeline
	if len(results) > 6 {
		results = results[:6]
	}

	return results, nil
}

// engine.ExtractLibraryName tries to extract a library/framework name from a query.
// Returns the library name and the cleaned query, or empty string if not detected.
func ExtractLibraryName(query string) string {
	q := strings.ToLower(query)

	// Known popular libraries/frameworks — order by specificity (longer first)
	libraries := []struct {
		pattern string
		name    string
	}{
		// JavaScript/TypeScript
		{"next.js", "next.js"}, {"nextjs", "next.js"},
		{"react native", "react-native"},
		{"react", "react"}, {"vue.js", "vue"}, {"vuejs", "vue"}, {"vue", "vue"},
		{"angular", "angular"}, {"svelte", "svelte"}, {"solid.js", "solid"},
		{"express.js", "express"}, {"express", "express"},
		{"nestjs", "nestjs"}, {"nuxt", "nuxt"}, {"remix", "remix"},
		{"tailwindcss", "tailwindcss"}, {"tailwind", "tailwindcss"},
		{"prisma", "prisma"}, {"drizzle", "drizzle-orm"},
		{"zod", "zod"}, {"trpc", "trpc"},
		{"playwright", "playwright"}, {"puppeteer", "puppeteer"},
		{"jest", "jest"}, {"vitest", "vitest"}, {"cypress", "cypress"},
		{"webpack", "webpack"}, {"vite", "vite"}, {"esbuild", "esbuild"},
		{"three.js", "three.js"}, {"threejs", "three.js"}, {"d3.js", "d3"}, {"d3", "d3"},
		{"socket.io", "socket.io"}, {"axios", "axios"},
		{"tanstack", "tanstack-query"}, {"react-query", "tanstack-query"},
		{"zustand", "zustand"}, {"jotai", "jotai"}, {"redux", "redux"},
		{"shadcn", "shadcn-ui"}, {"radix", "radix-ui"},
		{"astro", "astro"}, {"gatsby", "gatsby"},
		{"deno", "deno"}, {"bun", "bun"},
		{"hono", "hono"}, {"fastify", "fastify"}, {"koa", "koa"},
		// Python
		{"fastapi", "fastapi"}, {"django", "django"}, {"flask", "flask"},
		{"sqlalchemy", "sqlalchemy"}, {"pydantic", "pydantic"},
		{"celery", "celery"}, {"pytest", "pytest"},
		{"langchain", "langchain"}, {"llamaindex", "llama-index"},
		{"pandas", "pandas"}, {"numpy", "numpy"}, {"scipy", "scipy"},
		{"pytorch", "pytorch"}, {"tensorflow", "tensorflow"},
		{"scikit-learn", "scikit-learn"}, {"sklearn", "scikit-learn"},
		{"matplotlib", "matplotlib"}, {"streamlit", "streamlit"},
		{"httpx", "httpx"}, {"aiohttp", "aiohttp"},
		{"beautifulsoup", "beautifulsoup4"}, {"scrapy", "scrapy"},
		// Go
		{"gin ", "gin-gonic"}, {"gin-gonic", "gin-gonic"},
		{"echo ", "echo"}, {"fiber ", "fiber"},
		{"gorm", "gorm"}, {"ent ", "ent"},
		{"cobra", "cobra"}, {"viper", "viper"},
		// Rust
		{"tokio", "tokio"}, {"actix", "actix-web"}, {"axum", "axum"},
		{"serde", "serde"}, {"reqwest", "reqwest"},
		// Databases & infra
		{"supabase", "supabase"}, {"firebase", "firebase"},
		{"mongodb", "mongodb"}, {"mongoose", "mongoose"},
		{"redis", "redis"}, {"elasticsearch", "elasticsearch"},
		{"docker", "docker"}, {"kubernetes", "kubernetes"}, {"k8s", "kubernetes"},
		{"terraform", "terraform"}, {"pulumi", "pulumi"},
		// Other
		{"htmx", "htmx"}, {"alpine.js", "alpinejs"}, {"alpinejs", "alpinejs"},
		{"graphql", "graphql"}, {"apollo", "apollo-client"},
		{"stripe", "stripe"}, {"auth0", "auth0"},
	}

	for _, lib := range libraries {
		if strings.Contains(q, lib.pattern) {
			return lib.name
		}
	}
	return ""
}
