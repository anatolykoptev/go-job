// Package render provides a document-rendering adapter that converts
// Markdown and HTML content into PDF and raster-image formats.
//
// Sub-packages:
//   - render/typst — Typst+pandoc pipeline (zero chromedp deps)
//   - render/html  — goldmark Markdown→HTML pipeline
//   - render/chrome — chromedp Headless-Chrome PDF/image pipeline
package render
