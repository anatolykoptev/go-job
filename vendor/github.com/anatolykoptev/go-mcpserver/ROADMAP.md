# Roadmap

## v0.4.0 (done)

- ~~**Stateless/Stateful toggle**~~ — `Config.Stateless *bool`; nil defaults to true
- ~~**DisableMCP flag**~~ — `Config.DisableMCP bool`; skips `/mcp` route registration
- ~~**Flusher interface**~~ — `responseWriter` implements `http.Flusher`; extracted to `response_writer.go`
- ~~**os.Exit cleanup**~~ — done in v0.3.0 audit (error channel)
- ~~**Expose handler**~~ — `Build()` returns `(http.Handler, error)` for testing/embedding
- ~~**Consumer migrations**~~ — go-billing migrated; `WithRequestID()` context helper added
