// Package fileopt provides byte-level lossless optimization for PDFs and
// images by shelling out to system binaries (gs + qpdf for PDF, oxipng for
// PNG, cwebp for WebP). Missing binaries are non-fatal — callers receive the
// original bytes with a warn log, so optimization can be enabled
// unconditionally without host-setup concerns.
//
// Guarantees:
//   - Lossless by default: every stage is guarded so the returned bytes are
//     never larger than the input. If a stage produces a larger file (e.g.
//     cwebp on a smooth gradient), the original bytes are returned.
//   - Content-aware routing: text-only PDFs skip the ghostscript stage,
//     saving ~125ms of near-useless CPU while the qpdf structural pass
//     still runs.
//   - Per-stage Prometheus metrics: each subprocess (gs/qpdf/oxipng/cwebp)
//     reports calls/duration/ratio/bytes independently, so operators can see
//     the marginal contribution of stage-2 tools in addition to combined
//     reduction.
//
// Environment overrides (for custom binary paths):
//
//	FILEOPT_GS_PATH      — ghostscript
//	FILEOPT_QPDF_PATH    — qpdf
//	FILEOPT_OXIPNG_PATH  — oxipng
//	FILEOPT_CWEBP_PATH   — cwebp
//
// Typical usage:
//
//	optimized, err := fileopt.OptimizeBytes(ctx, data,
//	    fileopt.KindFromExt(filepath.Ext(path)),
//	    fileopt.LevelEbook, 80)
//
// Mount metrics on an HTTP server:
//
//	mux.Handle("/metrics", fileopt.MetricsHandler())
package fileopt
