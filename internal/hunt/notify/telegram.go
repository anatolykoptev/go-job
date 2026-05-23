// Package notify provides ingest-side Telegram notifications for hunt entries.
// Notifications fire on OutcomeCreated — any ingest path (search-tool, MCP, cron).
// This replaces the 3 hand-rolled monitor goroutines (bounty_monitor.go,
// freelance_monitor.go, security_monitor.go) that only covered the monitor path.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/anatolykoptev/go_job/internal/hunt"
)

// defaultBountyChatID is the Telegram chat ID used when BOUNTY_NOTIFY_CHAT_ID is unset.
// NIT: extracted from inline literal "428660" to a named constant for clarity.
const defaultBountyChatID = "428660"

// notifySemSize is the maximum number of concurrent Telegram send goroutines.
// Prevents unbounded fan-out: 50 new bounties = 50 simultaneous POSTs without this guard.
const notifySemSize = 8

// notifySem is a counting semaphore that bounds concurrent notify.send goroutines.
var notifySem = make(chan struct{}, notifySemSize)

// vaelorMsgRequest is the request body for vaelor's message tool API.
type vaelorMsgRequest struct {
	Content string `json:"content"`
	Channel string `json:"channel"`
	ChatID  string `json:"chat_id"`
}

// Notifier sends Telegram messages via the vaelor message tool.
// Methods are no-ops when URL is empty. Never construct with a nil pointer —
// NewFromEnv always returns a valid *Notifier (URL="" = disabled).
type Notifier struct {
	URL     string
	ChatID  string
	Client  *http.Client
	OnSend  func(outcome string) // optional metric hook: called with "sent" or "failed"
}

// NewFromEnv constructs a Notifier from VAELOR_NOTIFY_URL and BOUNTY_NOTIFY_CHAT_ID
// environment variables. Always returns non-nil; methods are no-ops if URL is empty.
// The nil-in-interface trap is avoided: callers can store a *Notifier unconditionally;
// the nil-URL guard inside each method prevents actual sends when URL is empty.
func NewFromEnv(url, chatID string) *Notifier {
	if chatID == "" {
		chatID = defaultBountyChatID
	}
	return &Notifier{
		URL:    url,
		ChatID: chatID,
		Client: &http.Client{Timeout: 10 * time.Second},
	}
}

// NotifyNewBounty sends a notification for a new bounty entry.
// Fire-and-forget: spawns a goroutine bounded by the notify semaphore.
func (n *Notifier) NotifyNewBounty(b hunt.Bounty) {
	if n == nil || n.URL == "" {
		return
	}
	msg := formatBountyMsg(b)
	go n.sendBounded(msg)
}

// NotifyNewJob sends a notification for a new job entry.
func (n *Notifier) NotifyNewJob(j hunt.Job) {
	if n == nil || n.URL == "" {
		return
	}
	msg := formatJobMsg(j)
	go n.sendBounded(msg)
}

// NotifyNewFreelance sends a notification for a new freelance entry.
func (n *Notifier) NotifyNewFreelance(f hunt.Freelance) {
	if n == nil || n.URL == "" {
		return
	}
	msg := formatFreelanceMsg(f)
	go n.sendBounded(msg)
}

// NotifyNewSecurity sends a notification for a new security program entry.
func (n *Notifier) NotifyNewSecurity(s hunt.Security) {
	if n == nil || n.URL == "" {
		return
	}
	msg := formatSecurityMsg(s)
	go n.sendBounded(msg)
}

// sendBounded acquires a semaphore slot before calling send, bounding concurrent POSTs.
func (n *Notifier) sendBounded(msg string) {
	notifySem <- struct{}{}
	defer func() { <-notifySem }()
	n.send(msg)
}

func (n *Notifier) send(msg string) {
	payload, _ := json.Marshal(vaelorMsgRequest{
		Content: msg,
		Channel: "telegram",
		ChatID:  n.ChatID,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.URL+"/api/tools/message", bytes.NewReader(payload))
	if err != nil {
		slog.Warn("hunt notify: build request failed", slog.Any("error", err))
		if n.OnSend != nil {
			n.OnSend("failed")
		}
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.Client.Do(req)
	if err != nil {
		slog.Warn("hunt notify: send failed", slog.Any("error", err))
		if n.OnSend != nil {
			n.OnSend("failed")
		}
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		slog.Warn("hunt notify: non-200 response",
			slog.Int("status", resp.StatusCode),
			slog.String("body", string(body)))
		if n.OnSend != nil {
			n.OnSend("failed")
		}
		return
	}
	if n.OnSend != nil {
		n.OnSend("sent")
	}
}

func formatBountyMsg(b hunt.Bounty) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "New Bounty")
	if b.AmountCents > 0 {
		fmt.Fprintf(&sb, " $%d", b.AmountCents/100)
	}
	fmt.Fprintf(&sb, "\n%s\n", b.Title)
	if len(b.Skills) > 0 {
		fmt.Fprintf(&sb, "Skills: %s\n", strings.Join(b.Skills, ", "))
	}
	sb.WriteString(b.URL)
	return sb.String()
}

func formatJobMsg(j hunt.Job) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[%s] %s\n", j.Source, j.Title)
	if j.Company != "" {
		fmt.Fprintf(&sb, "Company: %s\n", j.Company)
	}
	if j.SalaryMin > 0 || j.SalaryMax > 0 {
		fmt.Fprintf(&sb, "Salary: $%d–$%d\n", j.SalaryMin, j.SalaryMax)
	}
	sb.WriteString(j.URL)
	return sb.String()
}

func formatFreelanceMsg(f hunt.Freelance) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[%s] %s\n", f.Source, f.Title)
	if f.BudgetMax > 0 {
		fmt.Fprintf(&sb, "Budget: $%d\n", f.BudgetMax)
	}
	if len(f.Tags) > 0 {
		fmt.Fprintf(&sb, "Tags: %s\n", strings.Join(f.Tags, ", "))
	}
	sb.WriteString(f.URL)
	return sb.String()
}

func formatSecurityMsg(s hunt.Security) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "New Security Program [%s]\n%s\n", s.Platform, s.Name)
	if s.MaxBounty > 0 {
		fmt.Fprintf(&sb, "Max bounty: $%d\n", s.MaxBounty)
	}
	if len(s.Targets) > 0 {
		limit := 3
		if len(s.Targets) < limit {
			limit = len(s.Targets)
		}
		fmt.Fprintf(&sb, "Scope: %s\n", strings.Join(s.Targets[:limit], ", "))
	}
	sb.WriteString(s.URL)
	return sb.String()
}
