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

// vaelorMsgRequest is the request body for vaelor's message tool API.
type vaelorMsgRequest struct {
	Content string `json:"content"`
	Channel string `json:"channel"`
	ChatID  string `json:"chat_id"`
}

// Notifier sends Telegram messages via the vaelor message tool.
// A nil Notifier is safe — all methods are no-ops.
type Notifier struct {
	URL    string
	ChatID string
	Client *http.Client
}

// NewFromEnv constructs a Notifier from VAELOR_NOTIFY_URL and BOUNTY_NOTIFY_CHAT_ID
// environment variables. Returns nil if URL is empty (notifications disabled).
func NewFromEnv(url, chatID string) *Notifier {
	if url == "" {
		return nil
	}
	if chatID == "" {
		chatID = "428660"
	}
	return &Notifier{
		URL:    url,
		ChatID: chatID,
		Client: &http.Client{Timeout: 10 * time.Second},
	}
}

// NotifyNewBounty sends a notification for a new bounty entry.
// Fire-and-forget: runs in the caller's goroutine if already async, otherwise wraps in go.
func (n *Notifier) NotifyNewBounty(b hunt.Bounty) {
	if n == nil || n.URL == "" {
		return
	}
	msg := formatBountyMsg(b)
	go n.send(msg)
}

// NotifyNewJob sends a notification for a new job entry.
func (n *Notifier) NotifyNewJob(j hunt.Job) {
	if n == nil || n.URL == "" {
		return
	}
	msg := formatJobMsg(j)
	go n.send(msg)
}

// NotifyNewFreelance sends a notification for a new freelance entry.
func (n *Notifier) NotifyNewFreelance(f hunt.Freelance) {
	if n == nil || n.URL == "" {
		return
	}
	msg := formatFreelanceMsg(f)
	go n.send(msg)
}

// NotifyNewSecurity sends a notification for a new security program entry.
func (n *Notifier) NotifyNewSecurity(s hunt.Security) {
	if n == nil || n.URL == "" {
		return
	}
	msg := formatSecurityMsg(s)
	go n.send(msg)
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
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.Client.Do(req)
	if err != nil {
		slog.Warn("hunt notify: send failed", slog.Any("error", err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		slog.Warn("hunt notify: non-200 response",
			slog.Int("status", resp.StatusCode),
			slog.String("body", string(body)))
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
