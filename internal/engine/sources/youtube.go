package sources

// YouTube implementation is split across three files by responsibility:
//   youtube_innertube.go  — Innertube API types, constants, and low-level HTTP primitives
//   youtube_transcript.go — transcript fetching (engagement panel + ANDROID player fallback)
//   youtube_search.go     — video search (Data API v3 + ytInitialData scraping), parallel
//                           transcript fetching, and LLM summarization







