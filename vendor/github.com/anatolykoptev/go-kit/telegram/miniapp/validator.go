package miniapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Sentinel errors returned by validation functions.
var (
	// ErrInvalidSignature is returned when the HMAC-SHA256 signature does not match.
	ErrInvalidSignature = errors.New("miniapp: invalid initData signature")
	// ErrMissingHash is returned when the initData string has no "hash" parameter.
	ErrMissingHash = errors.New("miniapp: missing hash parameter")
	// ErrStaleAuthDate is returned when auth_date is older than the allowed maxAge.
	ErrStaleAuthDate = errors.New("miniapp: auth_date too old")
)

// InitData holds the parsed fields from a validated Telegram Mini App initData string.
// All fields are optional except AuthDate, which is always present in valid initData.
type InitData struct {
	// QueryID is the unique session ID used with answerWebAppQuery.
	QueryID string
	// User contains the current Telegram user, if present.
	User *User
	// AuthDate is the time at which the initData was generated.
	AuthDate time.Time
	// StartParam is the value of the startapp/startattach query parameter.
	StartParam string
	// ChatType is the type of chat from which the Mini App was opened.
	ChatType string
	// ChatInstance is the global identifier of the chat.
	ChatInstance string
	// Receiver is the chat partner in private-chat attachment-menu launches.
	Receiver *User
}

// User holds Telegram user identity fields from the Mini App initData.
type User struct {
	// ID is the Telegram user identifier.
	ID int64 `json:"id"`
	// FirstName is the user's first name.
	FirstName string `json:"first_name"`
	// LastName is the user's last name (optional).
	LastName string `json:"last_name,omitempty"`
	// Username is the user's @-handle (optional).
	Username string `json:"username,omitempty"`
	// LanguageCode is the IETF language tag of the user's client.
	LanguageCode string `json:"language_code,omitempty"`
	// IsPremium indicates whether the user has Telegram Premium.
	IsPremium bool `json:"is_premium,omitempty"`
	// PhotoURL is the URL of the user's profile photo (optional).
	PhotoURL string `json:"photo_url,omitempty"`
}

// ValidateInitData verifies the initData query string signature using the bot token.
// Returns parsed InitData if valid; one of ErrMissingHash or ErrInvalidSignature otherwise.
//
// The bot_token is passed in — it is the caller's responsibility to load it
// from a secure source (environment variable, secrets manager, etc.).
//
// Hash comparison uses crypto/subtle.ConstantTimeCompare to prevent timing attacks.
func ValidateInitData(initData, botToken string) (*InitData, error) {
	return validateWithTime(initData, botToken, 0, time.Now)
}

// ValidateInitDataWithMaxAge verifies the initData signature and additionally
// rejects payloads whose auth_date is older than maxAge.
// Returns ErrStaleAuthDate if the auth_date exceeds the allowed window.
func ValidateInitDataWithMaxAge(initData, botToken string, maxAge time.Duration) (*InitData, error) {
	return validateWithTime(initData, botToken, maxAge, time.Now)
}

// validateWithTime is the internal implementation; nowFn is injectable for testing.
func validateWithTime(initData, botToken string, maxAge time.Duration, nowFn func() time.Time) (*InitData, error) {
	q, err := url.ParseQuery(initData)
	if err != nil {
		return nil, errors.Join(ErrInvalidSignature, err)
	}

	receivedHash := q.Get("hash")
	if receivedHash == "" {
		return nil, ErrMissingHash
	}

	// Build data_check_string: sorted "key=value" pairs, excluding ONLY "hash".
	//
	// The "signature" field (ed25519, Bot API 7.x+) is INCLUDED in the
	// data_check_string. Telegram signs over the entire payload minus "hash",
	// and the OvyFlash/telegram-bot-api reference impl confirms this
	// (helper_methods.go::ValidateWebAppData excludes only "hash").
	//
	// Earlier doc-string here said "exclude signature per spec" — that was
	// based on a misreading; signatures from real iOS Bot API 9.6 clients
	// fail HMAC validation when signature is excluded (incident 2026-05-18,
	// debug-trace branch on oxpulse-admin).
	pairs := make([]string, 0, len(q))
	for k, vs := range q {
		if k == "hash" {
			continue
		}
		pairs = append(pairs, k+"="+vs[0])
	}
	sort.Strings(pairs)
	dataCheckString := strings.Join(pairs, "\n")

	// secret_key = HMAC-SHA256(key="WebAppData", msg=bot_token)
	skMac := hmac.New(sha256.New, []byte("WebAppData"))
	skMac.Write([]byte(botToken))
	secretKey := skMac.Sum(nil)

	// expected_hash = hex(HMAC-SHA256(key=secret_key, msg=data_check_string))
	msgMac := hmac.New(sha256.New, secretKey)
	msgMac.Write([]byte(dataCheckString))
	expectedHash := hex.EncodeToString(msgMac.Sum(nil))

	// Constant-time comparison to prevent timing-based hash oracle attacks.
	if subtle.ConstantTimeCompare([]byte(expectedHash), []byte(receivedHash)) != 1 {
		return nil, ErrInvalidSignature
	}

	return parseFields(q, maxAge, nowFn)
}

// parseFields converts the validated url.Values into an InitData struct.
// auth_date parsing errors are treated as invalid signature rather than a
// separate error to keep the public API surface minimal.
func parseFields(q url.Values, maxAge time.Duration, nowFn func() time.Time) (*InitData, error) {
	result := &InitData{
		QueryID:      q.Get("query_id"),
		StartParam:   q.Get("start_param"),
		ChatType:     q.Get("chat_type"),
		ChatInstance: q.Get("chat_instance"),
	}

	// auth_date is a Unix timestamp string.
	if raw := q.Get("auth_date"); raw != "" {
		var ts int64
		for _, c := range raw {
			if c < '0' || c > '9' {
				return nil, ErrInvalidSignature
			}
			ts = ts*10 + int64(c-'0')
		}
		result.AuthDate = time.Unix(ts, 0)
	}

	// MaxAge check: reject stale auth_date when a window is specified.
	if maxAge > 0 && !result.AuthDate.IsZero() {
		if nowFn().Sub(result.AuthDate) > maxAge {
			return nil, ErrStaleAuthDate
		}
	}

	// Parse user JSON blob if present.
	if userRaw := q.Get("user"); userRaw != "" {
		var u User
		if err := json.Unmarshal([]byte(userRaw), &u); err != nil {
			return nil, ErrInvalidSignature
		}
		result.User = &u
	}

	// Parse receiver JSON blob if present.
	if receiverRaw := q.Get("receiver"); receiverRaw != "" {
		var u User
		if err := json.Unmarshal([]byte(receiverRaw), &u); err != nil {
			return nil, ErrInvalidSignature
		}
		result.Receiver = &u
	}

	return result, nil
}
