// Package miniapp provides Telegram Mini App initData signature validation.
//
// Telegram Mini Apps pass an initData query string when a WebApp opens inside
// a bot. The server MUST validate the HMAC-SHA256 signature before trusting
// any user identity fields.
//
// Validation algorithm (per Telegram Bot API spec):
//
//  1. Parse initData as a URL-encoded query string.
//  2. Extract the "hash" parameter; return ErrMissingHash if absent.
//  3. Exclude "hash" (and "signature" if present) from the remaining pairs.
//  4. Sort remaining "key=value" pairs alphabetically and join with "\n"
//     to form the data_check_string.
//  5. Compute secret_key = HMAC-SHA256(key="WebAppData", msg=bot_token).
//  6. Compute expected_hash = hex(HMAC-SHA256(key=secret_key, msg=data_check_string)).
//  7. Compare expected_hash and received hash using crypto/subtle.ConstantTimeCompare.
//
// Reference: https://core.telegram.org/bots/webapps#validating-data-received-via-the-mini-app
//
// Algorithm cross-referenced with the MIT-licensed reference implementation:
// https://github.com/telegram-mini-apps/init-data-golang (MIT, Copyright 2022 Vladislav Kibenko).
// No code was copied; only the algorithm was verified for correctness.
//
// In addition to InitData validation, this package provides high-level helpers
// for Mini App server-side operations:
//
//   - Reply (answer.go): answerWebAppQuery — send an inline result back into
//     the Mini App session.
//   - SendInvoice / CreateInvoiceLink (invoice.go): Telegram Stars (XTR) +
//     classic provider invoices.
//   - SavePrepared (prepared.go): savePreparedInlineMessage — store a message
//     that a Mini App user can later share into any chat picker.
//
// Adapters that wrap *tgbotapi.BotAPI for each of these interfaces live in
// the sibling package telegram/tgapi5.
package miniapp
