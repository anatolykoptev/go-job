// cmd/gojob-admin is the operator-run account/key provisioning CLI (plan
// ADR-12). It is invoked over ssh against DATABASE_URL and owns the
// panel_accounts + mcp_api_keys lifecycle: there is no public registration
// path and no HTTP surface — this binary is the only provisioning interface.
//
// Usage:
//
//	DATABASE_URL=postgres://... gojob-admin account create --email E --name N [--password P] [--role user|admin] [--notify-chat-id ID]
//	                                             (omit --password to generate a temporary one, printed once)
//	DATABASE_URL=postgres://... gojob-admin account list
//	DATABASE_URL=postgres://... gojob-admin account activate (--id UUID | --email E)
//	DATABASE_URL=postgres://... gojob-admin account deactivate (--id UUID | --email E)
//	DATABASE_URL=postgres://... gojob-admin account set-password (--id UUID | --email E) [--password P]
//	DATABASE_URL=postgres://... gojob-admin key mint --account ID_OR_EMAIL --label L
//	DATABASE_URL=postgres://... gojob-admin key list [--account ID_OR_EMAIL]
//	DATABASE_URL=postgres://... gojob-admin key revoke (--id UUID | --prefix P)
//
// Bootstrap: run() invokes accounts.Bootstrap with an EMPTY OperatorSeed on
// every invocation — a fresh DATABASE_URL gets panel_accounts + the role CHECK
// + mcp_api_keys, while seedOperator no-ops so the CLI never env-seeds an
// operator (seeding is the boot path's job, not an ssh-side command's).
//
// Secret hygiene: `key mint` prints the plaintext token to stdout exactly once
// — it is the only place a usable credential ever appears (the table stores
// sha256 + an 8-char prefix). Generated account passwords print to stderr once
// the same way; the table stores only bcrypt hashes. Nothing here logs through slog, and no list
// command selects password_hash or key_hash.
//
// Scaffolding: stdlib flag subcommands, matching the repo's other cmd/
// entries (migrate-tracker, migrate-application-pdfs) — no go-kit/cli package
// is vendored.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/anatolykoptev/go-panel/auth"
	"github.com/anatolykoptev/go_job/internal/accounts"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(context.Background(), os.Stdout, os.Stderr, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "gojob-admin:", err)
		os.Exit(1)
	}
}

// run is the testable entrypoint: stdout carries ids/tokens/tables, stderr
// carries warnings and flag-usage output. Errors propagate to main for a
// single-line stderr report and a non-zero exit.
func run(ctx context.Context, out, errOut io.Writer, args []string) error {
	if len(args) < 2 {
		usage(errOut)
		return errors.New("expected a command: account create|list|activate|deactivate|set-password | key mint|list|revoke")
	}

	// Every handler gets its parsed FlagSet plus a closure over its validated
	// inputs, so flags parse BEFORE any DB connection — `--help` and usage
	// errors never require DATABASE_URL.
	type cmdFn func(ctx context.Context, pool *pgxpool.Pool, out, errOut io.Writer) error
	var fn cmdFn

	switch args[0] + " " + args[1] {
	case "account create":
		fs := newFlagSet("account create", errOut)
		email := fs.String("email", "", "account email — the bcrypt-login identifier (required)")
		name := fs.String("name", "", "display name")
		password := fs.String("password", "",
			"bcrypt-login password; omit to auto-generate a temporary one (printed once — the user rotates it in the cabinet)")
		role := fs.String("role", "user",
			"account role: 'user' or 'admin'; 'owner' is never assignable — the panel_accounts CHECK rejects it")
		notifyChat := fs.Int64("notify-chat-id", 0,
			"Telegram chat id for hunt notifications; stored only once account_hunt_settings exists (lands in P3)")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if *email == "" {
			return errors.New("account create: --email is required")
		}
		if err := validateRole(*role); err != nil {
			return err
		}
		fn = func(ctx context.Context, pool *pgxpool.Pool, out, errOut io.Writer) error {
			return accountCreate(ctx, pool, out, errOut, *email, *name, *password, *role, *notifyChat)
		}
	case "account list":
		fs := newFlagSet("account list", errOut)
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		fn = func(ctx context.Context, pool *pgxpool.Pool, out, _ io.Writer) error {
			return accountList(ctx, pool, out)
		}
	case "account deactivate":
		fs := newFlagSet("account deactivate", errOut)
		id := fs.String("id", "", "account UUID")
		email := fs.String("email", "", "account email")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		ref, err := exactlyOneRef("account deactivate", *id, *email)
		if err != nil {
			return err
		}
		fn = func(ctx context.Context, pool *pgxpool.Pool, out, _ io.Writer) error {
			return accountSetActiveCmd(ctx, pool, out, ref, false)
		}
	case "account activate":
		fs := newFlagSet("account activate", errOut)
		id := fs.String("id", "", "account UUID")
		email := fs.String("email", "", "account email")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		ref, err := exactlyOneRef("account activate", *id, *email)
		if err != nil {
			return err
		}
		fn = func(ctx context.Context, pool *pgxpool.Pool, out, _ io.Writer) error {
			return accountSetActiveCmd(ctx, pool, out, ref, true)
		}
	case "account set-password":
		ref, password, err := setPasswordFlags(args[2:], errOut)
		if err != nil {
			return err
		}
		fn = func(ctx context.Context, pool *pgxpool.Pool, out, errOut io.Writer) error {
			return accountSetPassword(ctx, pool, out, ref, password)
		}
	case "key mint":
		fs := newFlagSet("key mint", errOut)
		acct := fs.String("account", "", "owning account: UUID or email (required)")
		label := fs.String("label", "", "operator-facing key label (required)")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if *acct == "" || *label == "" {
			return errors.New("key mint: --account and --label are required")
		}
		fn = func(ctx context.Context, pool *pgxpool.Pool, out, errOut io.Writer) error {
			return keyMint(ctx, pool, out, errOut, *acct, *label)
		}
	case "key list":
		fs := newFlagSet("key list", errOut)
		acct := fs.String("account", "", "restrict to one account: UUID or email")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		fn = func(ctx context.Context, pool *pgxpool.Pool, out, _ io.Writer) error {
			return keyList(ctx, pool, out, *acct)
		}
	case "key revoke":
		fs := newFlagSet("key revoke", errOut)
		id := fs.String("id", "", "key UUID")
		prefix := fs.String("prefix", "", "key_prefix (the first 8 token chars) — refuses when it matches >1 active key")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		ref, err := exactlyOneRef("key revoke", *id, *prefix)
		if err != nil {
			return err
		}
		byPrefix := *prefix != ""
		fn = func(ctx context.Context, pool *pgxpool.Pool, out, _ io.Writer) error {
			return keyRevoke(ctx, pool, out, ref, byPrefix)
		}
	default:
		usage(errOut)
		return fmt.Errorf("unknown command %q %q", args[0], args[1])
	}

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL not set")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect db: %w", err)
	}
	defer pool.Close()

	// Empty OperatorSeed → schema only. Bootstrap runs EnsureSchema, the role
	// CHECK migration and the mcp_api_keys DDL in order, then seedOperator
	// no-ops on the empty seed — the CLI provisions schema, never operators.
	if _, _, err := accounts.Bootstrap(ctx, pool, accounts.OperatorSeed{}); err != nil {
		return fmt.Errorf("bootstrap accounts schema: %w", err)
	}
	return fn(ctx, pool, out, errOut)
}

func newFlagSet(name string, errOut io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("gojob-admin "+name, flag.ContinueOnError)
	fs.SetOutput(errOut)
	return fs
}

// setPasswordFlags parses `account set-password` flags: (--id UUID | --email E)
// plus an optional --password (omitted -> a temporary one is generated).
func setPasswordFlags(args []string, errOut io.Writer) (ref, password string, err error) {
	fs := newFlagSet("account set-password", errOut)
	id := fs.String("id", "", "account UUID")
	email := fs.String("email", "", "account email")
	pw := fs.String("password", "", "new bcrypt-login password (omit -> generate a temporary one)")
	if err := fs.Parse(args); err != nil {
		return "", "", err
	}
	ref, err = exactlyOneRef("account set-password", *id, *email)
	if err != nil {
		return "", "", err
	}
	return ref, *pw, nil
}

// genTempPassword returns a 24-char URL-safe random password — long enough
// for the 10..72 policy with margin, short enough to relay in chat.
func genTempPassword() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// accountSetPassword backs `account set-password` — rotates the bcrypt hash
// for an existing account (operator recovery path; the in-cabinet self-serve
// flow stays the user's own).
func accountSetPassword(ctx context.Context, pool *pgxpool.Pool, out io.Writer, ref, password string) error {
	if password == "" {
		generated, err := genTempPassword()
		if err != nil {
			return fmt.Errorf("account set-password: generate temp password: %w", err)
		}
		password = generated
		fmt.Fprintf(out, "temporary password (shown once): %s\n", password)
	}
	if len(password) < 10 || len(password) > 72 {
		return errors.New("account set-password: password must be 10-72 characters")
	}
	id, err := accounts.ResolveAccountID(ctx, pool, ref)
	if err != nil {
		return fmt.Errorf("account set-password: %w", err)
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("account set-password: hash: %w", err)
	}
	if err := auth.NewPgxAccountStore(pool).UpdatePasswordHash(ctx, id.String(), hash); err != nil {
		return fmt.Errorf("account set-password: %w", err)
	}
	fmt.Fprintf(out, "password updated: %s\n", id)
	return nil
}

func usage(w io.Writer) {
	fmt.Fprint(w, `gojob-admin — operator account/key provisioning (reads DATABASE_URL)

  account create --email E --name N [--password P] [--role user|admin] [--notify-chat-id ID]
                                          omit --password to generate a temporary one (printed once)
  account list
  account activate (--id UUID | --email E)
  account deactivate (--id UUID | --email E)
  account set-password (--id UUID | --email E) [--password P]   omit to generate
  key mint --account ID_OR_EMAIL --label L     prints the token ONCE — store it
  key list [--account ID_OR_EMAIL]
  key revoke (--id UUID | --prefix P)
`)
}

// validateRole is the single gate for --role: exactly {user, admin}. Anything
// else — including the role the panel_accounts CHECK bans — is rejected before
// any SQL runs. The DB CHECK remains the second wall.
func validateRole(role string) error {
	switch role {
	case "user", "admin":
		return nil
	default:
		return fmt.Errorf("account create: role %q not allowed — only 'user' or 'admin' ('owner' is never assignable)", role)
	}
}

// exactlyOneRef enforces the --id|--email / --id|--prefix mutual exclusivity
// shared by deactivate and revoke.
func exactlyOneRef(cmd, a, b string) (string, error) {
	if (a == "") == (b == "") {
		return "", fmt.Errorf("%s: exactly one selector flag is required", cmd)
	}
	if a != "" {
		return a, nil
	}
	return b, nil
}

func accountCreate(ctx context.Context, pool *pgxpool.Pool, out, errOut io.Writer,
	email, name, password, role string, notifyChatID int64) error {

	if password == "" {
		generated, err := genTempPassword()
		if err != nil {
			return fmt.Errorf("account create: generate temp password: %w", err)
		}
		password = generated
		// Printed to stderr — the operator relays it once; the account model
		// has no key-only shape, so a password must always exist.
		fmt.Fprintf(errOut, "temporary password (shown once — user must change it in the cabinet): %s\n", password)
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("account create: hash password: %w", err)
	}
	id, created, err := accounts.CreateAccount(ctx, pool, email, name, hash, role)
	if err != nil {
		return fmt.Errorf("account create: %w", err)
	}
	if !created {
		fmt.Fprintf(errOut, "warning: account %s already exists (id %s) — left unchanged\n", email, id)
	}
	if notifyChatID != 0 {
		stored, err := accounts.SetNotifyChatID(ctx, pool, id, notifyChatID)
		if err != nil {
			return fmt.Errorf("account create: %w", err)
		}
		if !stored {
			fmt.Fprintln(errOut,
				"warning: hunt settings table not yet present — set notify_chat_id post-P3")
		}
	}
	if created {
		fmt.Fprintf(out, "id: %s\nemail: %s\nrole: %s\n", id, email, role)
	} else {
		// On conflict the stored row is authoritative — its role may differ
		// from --role, so report existence instead of the requested values.
		fmt.Fprintf(out, "id: %s\nemail: %s\nexists: true\n", id, email)
	}
	return nil
}

func accountList(ctx context.Context, pool *pgxpool.Pool, out io.Writer) error {
	rows, err := accounts.ListAccountRows(ctx, pool)
	if err != nil {
		return fmt.Errorf("account list: %w", err)
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tEMAIL\tNAME\tROLE\tACTIVE\tCREATED_AT")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%t\t%s\n",
			r.ID, r.Email, r.Name, r.Role, r.Active, r.CreatedAt.Format(time.RFC3339))
	}
	return tw.Flush()
}

// accountSetActiveCmd backs `account activate` and `account deactivate` —
// both are SetActive on the resolved row. Idempotent: flipping an
// already-in-that-state row is a no-op UPDATE (RowsAffected ≥1 all the same).
func accountSetActiveCmd(ctx context.Context, pool *pgxpool.Pool, out io.Writer, ref string, active bool) error {
	verb, past := "account deactivate", "deactivated"
	if active {
		verb, past = "account activate", "activated"
	}
	id, err := accounts.ResolveAccountID(ctx, pool, ref)
	if err != nil {
		return fmt.Errorf("%s: %w", verb, err)
	}
	if err := auth.NewPgxAccountStore(pool).SetActive(ctx, id.String(), active); err != nil {
		return fmt.Errorf("%s: %w", verb, err)
	}
	fmt.Fprintf(out, "%s: %s\n", past, id)
	return nil
}

func keyMint(ctx context.Context, pool *pgxpool.Pool, out, errOut io.Writer, accountRef, label string) error {
	id, err := accounts.ResolveAccountID(ctx, pool, accountRef)
	if err != nil {
		return fmt.Errorf("key mint: %w", err)
	}
	token, err := accounts.NewKeyStore(pool).Mint(ctx, id, label)
	if err != nil {
		return fmt.Errorf("key mint: %w", err)
	}
	// The plaintext token is printed ONCE, here, on stdout — it is stored as
	// sha256 only and is unrecoverable after this command exits. Never log it.
	fmt.Fprintln(errOut, "note: the token below is shown once — store it now")
	fmt.Fprintf(out, "prefix: %s\ntoken: %s\n", accounts.KeyPrefix(token), token)
	return nil
}

func keyList(ctx context.Context, pool *pgxpool.Pool, out io.Writer, accountRef string) error {
	accountID := uuid.Nil
	if accountRef != "" {
		id, err := accounts.ResolveAccountID(ctx, pool, accountRef)
		if err != nil {
			return fmt.Errorf("key list: %w", err)
		}
		accountID = id
	}
	keys, err := accounts.NewKeyStore(pool).ListKeys(ctx, accountID)
	if err != nil {
		return fmt.Errorf("key list: %w", err)
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tPREFIX\tLABEL\tCREATED_AT\tLAST_USED_AT\tREVOKED_AT")
	for _, k := range keys {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			k.ID, k.Prefix, k.Label,
			k.CreatedAt.Format(time.RFC3339), fmtTime(k.LastUsedAt), fmtTime(k.RevokedAt))
	}
	return tw.Flush()
}

func keyRevoke(ctx context.Context, pool *pgxpool.Pool, out io.Writer, ref string, byPrefix bool) error {
	ks := accounts.NewKeyStore(pool)
	var id uuid.UUID
	if byPrefix {
		ids, err := ks.ActiveKeyIDsByPrefix(ctx, ref)
		if err != nil {
			return fmt.Errorf("key revoke: %w", err)
		}
		switch len(ids) {
		case 0:
			return fmt.Errorf("key revoke: no active key with prefix %q", ref)
		case 1:
			id = ids[0]
		default:
			return fmt.Errorf("key revoke: ambiguous prefix %q matches %d active keys — re-run with --id", ref, len(ids))
		}
	} else {
		parsed, err := uuid.Parse(ref)
		if err != nil {
			return fmt.Errorf("key revoke: --id %q is not a UUID", ref)
		}
		id = parsed
	}
	if err := ks.Revoke(ctx, id); err != nil {
		return fmt.Errorf("key revoke: %w", err)
	}
	fmt.Fprintf(out, "revoked: %s\n", id)
	return nil
}

func fmtTime(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Format(time.RFC3339)
}
