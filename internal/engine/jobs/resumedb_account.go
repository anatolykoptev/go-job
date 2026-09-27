package jobs

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ResumeAccount is the account-scoped view of ResumeDB (plan ADR-6/ADR-15,
// P4). resume_persons is the account-owned hub; every child row (experiences,
// skills, projects, achievements, educations, certifications, domains,
// methodologies, upwork_*) and every resume_vectors row is owned transitively
// or directly. Binding via ForAccount makes unscoped statements unreachable:
// person reads/writes carry account_id predicates, child operations prove the
// parent person is owned by the bound account, and AGE graph statements carry
// the account as a node/edge property.
type ResumeAccount struct {
	db  *ResumeDB
	aid uuid.UUID
}

// ForAccount binds the resume store to one panel_accounts.id.
//
// uuid.Nil binds a view that fails closed: reads match nothing (the
// '00000000-0000-0000-0000-000000000000' account_id never exists) and
// writes/destructive operations return ErrNoAccountScope.
func (db *ResumeDB) ForAccount(aid uuid.UUID) *ResumeAccount {
	return &ResumeAccount{db: db, aid: aid}
}

// ErrNoAccountScope is returned by write/destructive operations on a
// Nil-account facade — account identity must never silently widen.
var ErrNoAccountScope = errors.New("jobs: account scope required (uuid.Nil is not a writable account)")

// AccountID returns the bound account UUID.
func (a *ResumeAccount) AccountID() uuid.UUID { return a.aid }

// writable reports whether the bound account may perform writes/deletes.
func (a *ResumeAccount) writable() bool { return a.aid != uuid.Nil }

// aidStr renders the bound account for AGE cypher interpolation. uuid.UUID
// values have a fixed canonical format — safe to embed as a string literal.
func (a *ResumeAccount) aidStr() string { return a.aid.String() }

// conn picks tx > pool, mirroring ResumeDB.conn for facade methods.
func (a *ResumeAccount) conn(ctx context.Context) execConn { return a.db.conn(ctx) }
