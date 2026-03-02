# Roadmap

## v0.4.0

- **Stateless/Stateful toggle** — expose `StreamableHTTPOptions` in Config instead of hardcoded `Stateless: true`
- **DisableMCP flag** — allow using go-mcpserver purely for HTTP bootstrap without registering `/mcp` routes
- **Flusher interface** — `responseWriter` should implement `http.Flusher` for SSE support through middleware
- **os.Exit cleanup** — replace `os.Exit(1)` in ListenAndServe goroutine with proper error channel
- **Expose handler** — add `Build()` method returning `http.Handler` for testing without starting a server
- **Consumer migrations** — migrate gigiena-teksta (~500 lines boilerplate) and go-billing (~120 lines) to go-mcpserver
