package tgapi5

import (
	"context"
	"encoding/json"
	"errors"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"

	"github.com/anatolykoptev/go-kit/telegram/miniapp"
)

// BotInvoiceSender adapts *tgbotapi.BotAPI to the miniapp.Sender interface.
// It supports SendInvoice (via bot.Send) and CreateInvoiceLink (via bot.Request),
// both honouring the caller's context via RequestWithContext.
type BotInvoiceSender struct {
	bot *tgbotapi.BotAPI
}

// NewInvoiceSender returns a BotInvoiceSender backed by bot.
// It satisfies miniapp.Sender.
func NewInvoiceSender(bot *tgbotapi.BotAPI) *BotInvoiceSender {
	return &BotInvoiceSender{bot: bot}
}

// SendInvoice sends a Telegram invoice using bot.RequestWithContext.
// Implements miniapp.Sender.
func (s *BotInvoiceSender) SendInvoice(ctx context.Context, cfg tgbotapi.InvoiceConfig) (*tgbotapi.Message, error) {
	resp, err := s.bot.RequestWithContext(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, errors.New("tgapi5: nil response from BotAPI")
	}
	var msg tgbotapi.Message
	if err := json.Unmarshal(resp.Result, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

// CreateInvoiceLink creates a Telegram invoice link using bot.RequestWithContext.
// Implements miniapp.Sender.
func (s *BotInvoiceSender) CreateInvoiceLink(ctx context.Context, cfg tgbotapi.InvoiceLinkConfig) (string, error) {
	resp, err := s.bot.RequestWithContext(ctx, cfg)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", errors.New("tgapi5: nil response from BotAPI")
	}
	var link string
	if err := json.Unmarshal(resp.Result, &link); err != nil {
		return "", err
	}
	return link, nil
}

// BotWebAppAnswerer adapts *tgbotapi.BotAPI to the miniapp.WebAppAnswerer interface.
type BotWebAppAnswerer struct {
	bot *tgbotapi.BotAPI
}

// NewWebAppAnswerer returns a BotWebAppAnswerer backed by bot.
// It satisfies miniapp.WebAppAnswerer.
func NewWebAppAnswerer(bot *tgbotapi.BotAPI) *BotWebAppAnswerer {
	return &BotWebAppAnswerer{bot: bot}
}

// AnswerWebAppQuery answers a Web App query identified by queryID.
// Implements miniapp.WebAppAnswerer.
func (a *BotWebAppAnswerer) AnswerWebAppQuery(
	ctx context.Context,
	queryID string,
	result tgbotapi.InlineQueryResult,
) (*tgbotapi.SentWebAppMessage, error) {
	cfg := tgbotapi.AnswerWebAppQueryConfig{
		WebAppQueryID: queryID,
		Result:        result,
	}
	resp, err := a.bot.RequestWithContext(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, errors.New("tgapi5: nil response from BotAPI")
	}
	var msg tgbotapi.SentWebAppMessage
	if err := json.Unmarshal(resp.Result, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

// BotPreparedSender adapts *tgbotapi.BotAPI to the miniapp.PreparedSender
// interface. It dispatches via bot.MakeRequestWithContext to honour context,
// mirroring the body of the SDK's SavePreparedInlineMessage generic helper
// (which calls bot.Request and therefore cannot propagate a context).
//
// The SDK's generic SavePreparedInlineMessage[T] requires T to satisfy the
// InlineQueryResults union constraint; since the interface takes
// tgbotapi.InlineQueryResult (= any), we build Params manually instead
// of instantiating the generic config.
type BotPreparedSender struct {
	bot *tgbotapi.BotAPI
}

// NewPreparedSender returns a BotPreparedSender backed by bot.
// It satisfies miniapp.PreparedSender.
func NewPreparedSender(bot *tgbotapi.BotAPI) *BotPreparedSender {
	return &BotPreparedSender{bot: bot}
}

// SavePreparedInlineMessage implements miniapp.PreparedSender.
// It builds the request params manually (mirroring SDK configs.go params())
// and calls bot.MakeRequestWithContext so the caller's context is honoured.
func (s *BotPreparedSender) SavePreparedInlineMessage(
	ctx context.Context,
	userID int64,
	result tgbotapi.InlineQueryResult,
	opts miniapp.PreparedOptions,
) (tgbotapi.PreparedInlineMessage, error) {
	p := make(tgbotapi.Params)
	p.AddNonZero64("user_id", userID)
	if err := p.AddInterface("result", result); err != nil {
		return tgbotapi.PreparedInlineMessage{}, err
	}
	p.AddBool("allow_user_chats", opts.AllowUserChats)
	p.AddBool("allow_bot_chats", opts.AllowBotChats)
	p.AddBool("allow_group_chats", opts.AllowGroupChats)
	p.AddBool("allow_channel_chats", opts.AllowChannelChats)

	resp, err := s.bot.MakeRequestWithContext(ctx, "savePreparedInlineMessage", p)
	if err != nil {
		return tgbotapi.PreparedInlineMessage{}, err
	}
	if resp == nil {
		return tgbotapi.PreparedInlineMessage{}, errors.New("tgapi5: nil response from BotAPI")
	}
	var out tgbotapi.PreparedInlineMessage
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		return tgbotapi.PreparedInlineMessage{}, err
	}
	return out, nil
}

// Compile-time interface satisfaction checks.
var (
	_ miniapp.Sender         = (*BotInvoiceSender)(nil)
	_ miniapp.WebAppAnswerer = (*BotWebAppAnswerer)(nil)
	_ miniapp.PreparedSender = (*BotPreparedSender)(nil)
)
