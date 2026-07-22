// Package notify provides two opinionated, non-conflatable notification sinks
// for Go services.
//
// # Two sinks, one rule each
//
// AlertSink is the ONLY sanctioned way for Go code to raise an ops alert.
// It speaks directly to dozor's alertmanager-v4 webhook and exposes no method
// that can reach api.telegram.org. This makes the governed notification path
// non-bypassable by construction: a caller cannot emit a raw Telegram message
// through AlertSink, and the Severity enum prevents accidental off-vocabulary
// severities — explicit casts like Severity("warn") are still syntactically
// possible but require deliberate effort.
//
// ProductSink delivers per-event product notifications (new bounty, KOL
// mention, etc.) via an own bot token, with bounded-rate fan-out, retry, and
// dead-letter. It wraps the existing telegram/broadcast.Pacer and
// telegram/tgapi5.BotSender; HTML→plaintext fallback is applied on
// parse-entity errors (borrowed from go-hully's send pattern).
//
// # Metric names
//
// Both sinks bump counters on every attempt so failures are never silent.
// Counters are pre-touched at construction time so all series exist from t=0
// and rate() queries read 0 (not "no data") during healthy operation:
//
//   - AlertSink:   "notify_alert_total{severity=<level>,result=<ok|error>}"
//   - ProductSink: "notify_product_total{result=<sent|failed>}"
//
// Pass a *metrics.Registry from metrics.FromEnv to wire into Prometheus.
// nil is accepted on every constructor (counters become no-ops).
//
// # Severity routing (dozor gateway behaviour)
//
// dozor/cmd/dozor/gateway_alertmanager.go converts the severity label as
// follows (verified 2026-06-24):
//
//   - "critical" or "error" → AlertCritical  (immediate push, hourly re-page)
//   - "warning"             → AlertWarning   (5-min hold, 12-hour re-page)
//   - anything else         → AlertWarning   (default case)
//
// This means Info currently routes as Warning at dozor's gateway — there is no
// "info" case in the switch, so it falls to the default. True null-routing
// (suppressed Telegram delivery for informational events) would require adding
// an info case in dozor's gateway; that change is out of scope here.
// Use Info only when you want the alert on record but accept Warning-tier
// delivery until dozor is updated.
//
// # Alert field routing (dozor gateway behaviour)
//
// dozor's gateway derives its internal service name from labels["alertname"],
// not labels["service"] or labels["instance"]. The Service and Instance fields
// in Alert are emitted as plain passthrough labels in the alertmanager payload
// and are ignored by dozor's gateway. They may be consumed by downstream
// alertmanager routing rules or by future dozor versions.
//
// # Env vars
//
//	DOZOR_WEBHOOK_URL        AlertSink default: http://127.0.0.1:8765/webhook/alertmanager
//	DOZOR_WEBHOOK_TOKEN      AlertSink bearer token (optional; loopback usually unenforced)
//	TELEGRAM_BOT_TOKEN       ProductSink bot token (required)
//	<PREFIX>_NOTIFY_CHAT_ID  ProductSink default chat ID, e.g. BOUNTY_NOTIFY_CHAT_ID=428660
package notify
