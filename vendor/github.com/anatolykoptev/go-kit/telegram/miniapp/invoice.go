package miniapp

import (
	"context"
	"errors"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
)

// Sentinel errors returned by invoice validation.
var (
	// ErrInvalidStarsConfig is returned when Currency is "XTR" but ProviderToken is non-empty.
	// Telegram Stars payments must not include a provider token.
	ErrInvalidStarsConfig = errors.New("miniapp: XTR currency requires empty ProviderToken")

	// ErrInvalidProviderToken is returned when Currency is not "XTR" but ProviderToken is empty.
	// Non-Stars payments require a payment provider token.
	ErrInvalidProviderToken = errors.New("miniapp: non-XTR currency requires non-empty ProviderToken")

	// ErrInvalidTitle is returned when InvoiceParams.Title is empty.
	ErrInvalidTitle = errors.New("miniapp: invoice Title must not be empty")

	// ErrInvalidPrices is returned when InvoiceParams.Prices is nil or empty.
	ErrInvalidPrices = errors.New("miniapp: invoice Prices must not be empty")

	// ErrInvoiceLinkChatIDNotAllowed is returned by CreateInvoiceLink when ChatID != 0.
	// Invoice links are not associated with a specific chat — pass ChatID=0.
	ErrInvoiceLinkChatIDNotAllowed = errors.New("miniapp: ChatID must be 0 for invoice links (links are not chat-specific)")

	// ErrInvoiceLinkMessageThreadNotAllowed is returned by CreateInvoiceLink when
	// MessageThread != 0. Invoice links are not posted to forum topics.
	ErrInvoiceLinkMessageThreadNotAllowed = errors.New("miniapp: MessageThread must be 0 for invoice links (links are not forum-topic-specific)")
)

// Sender is the minimal sender interface for sending invoices and creating
// invoice links. Callers wrap *tgbotapi.BotAPI with an adapter that adds
// context support.
type Sender interface {
	SendInvoice(ctx context.Context, cfg tgbotapi.InvoiceConfig) (*tgbotapi.Message, error)
	CreateInvoiceLink(ctx context.Context, cfg tgbotapi.InvoiceLinkConfig) (string, error)
}

// InvoiceParams holds the fields required to send a Telegram invoice or
// create an invoice link.
//
// Currency rules:
//   - Set Currency to "XTR" and ProviderToken to "" for Telegram Stars payments.
//   - Set Currency to an ISO-4217 code and ProviderToken to the provider's token
//     for all other payment methods.
//
// MessageThread is only used by SendInvoice (forum-topic thread ID).
// CreateInvoiceLink rejects non-zero MessageThread with ErrInvoiceLinkMessageThreadNotAllowed.
//
// ChatID is only used by SendInvoice (target chat).
// CreateInvoiceLink rejects non-zero ChatID with ErrInvoiceLinkChatIDNotAllowed.
type InvoiceParams struct {
	// ChatID is the target chat for sendInvoice. Must be 0 for CreateInvoiceLink.
	ChatID int64
	// Title is the invoice title shown to the user. Required.
	Title string
	// Description is the invoice description. Required.
	Description string
	// Payload is the internal payload passed back on successful payment. Required.
	Payload string
	// ProviderToken is the payment provider token. Must be empty for XTR (Stars).
	ProviderToken string
	// Currency is the three-letter ISO-4217 currency code, or "XTR" for Stars.
	Currency string
	// Prices lists the price breakdown (label + amount in the smallest currency unit).
	Prices []tgbotapi.LabeledPrice
	// MessageThread is the forum-topic thread ID for sendInvoice. Zero means no thread.
	// Must be 0 for CreateInvoiceLink.
	MessageThread int
}

// validateInvoiceParams checks the business rules for InvoiceParams and returns
// a sentinel error on the first violated constraint.
func validateInvoiceParams(p InvoiceParams) error {
	if p.Title == "" {
		return ErrInvalidTitle
	}
	if len(p.Prices) == 0 {
		return ErrInvalidPrices
	}
	if p.Currency == "XTR" && p.ProviderToken != "" {
		return ErrInvalidStarsConfig
	}
	if p.Currency != "XTR" && p.ProviderToken == "" {
		return ErrInvalidProviderToken
	}
	return nil
}

// validateInvoiceLinkParams extends validateInvoiceParams with link-specific
// constraints: ChatID and MessageThread must be zero (invoice links are not
// chat-specific or forum-topic-specific).
func validateInvoiceLinkParams(p InvoiceParams) error {
	if err := validateInvoiceParams(p); err != nil {
		return err
	}
	if p.ChatID != 0 {
		return ErrInvoiceLinkChatIDNotAllowed
	}
	if p.MessageThread != 0 {
		return ErrInvoiceLinkMessageThreadNotAllowed
	}
	return nil
}

// SendInvoice validates p and sends a Telegram invoice to the chat specified
// in p.ChatID. On success it returns the sent Message.
//
// Validation errors (ErrInvalidTitle, ErrInvalidPrices, ErrInvalidStarsConfig,
// ErrInvalidProviderToken) are returned before the Sender is called.
func SendInvoice(ctx context.Context, s Sender, p InvoiceParams) (*tgbotapi.Message, error) {
	if err := validateInvoiceParams(p); err != nil {
		return nil, err
	}
	cfg := tgbotapi.InvoiceConfig{
		BaseChat: tgbotapi.BaseChat{
			ChatConfig:      tgbotapi.ChatConfig{ChatID: p.ChatID},
			MessageThreadID: p.MessageThread,
		},
		Title:         p.Title,
		Description:   p.Description,
		Payload:       p.Payload,
		ProviderToken: p.ProviderToken,
		Currency:      p.Currency,
		Prices:        p.Prices,
	}
	return s.SendInvoice(ctx, cfg)
}

// CreateInvoiceLink validates p and creates a Telegram invoice link that can
// be shared with users. On success it returns the invoice URL.
//
// Link-specific validation (before calling Sender):
//   - p.ChatID must be 0 (returns ErrInvoiceLinkChatIDNotAllowed if non-zero).
//   - p.MessageThread must be 0 (returns ErrInvoiceLinkMessageThreadNotAllowed if non-zero).
//
// General validation errors (ErrInvalidTitle, ErrInvalidPrices, ErrInvalidStarsConfig,
// ErrInvalidProviderToken) are also returned before the Sender is called.
func CreateInvoiceLink(ctx context.Context, s Sender, p InvoiceParams) (string, error) {
	if err := validateInvoiceLinkParams(p); err != nil {
		return "", err
	}
	cfg := tgbotapi.InvoiceLinkConfig{
		Title:         p.Title,
		Description:   p.Description,
		Payload:       p.Payload,
		ProviderToken: p.ProviderToken,
		Currency:      p.Currency,
		Prices:        p.Prices,
	}
	return s.CreateInvoiceLink(ctx, cfg)
}
