package jobserver

import kitembed "github.com/anatolykoptev/go-kit/embed"

// EmbedClients is the pair of embed clients built at the production wiring
// site (main.go) for one embed server URL. Both clients hit the same server
// with DIFFERENT budgets; the split is the contract NewEmbedClients enforces.
//
// Gate is the relevance gate's OWN client: built via NewEmbedClient, so its
// per-request timeout, retry envelope, and chunk size are derived from
// jobSearchRelevanceTimeout (EmbedClientBudgetOpts is applied). Scoped to the
// gate via SetRelevanceEmbedClient.
//
// Shared is the package-level singleton consumed by algora ingest,
// resume-vector sync, and profile sync (jobs.SetEmbedClient): built via
// kitembed.NewClient with ONLY the base opts so it keeps kitembed's library
// defaults (defaultRetryPolicy: 3 attempts; 30s per-request timeout). The
// gate's budgets MUST NOT leak onto these background jobs.
//
// GateErr / SharedErr carry each client's construction error independently so
// a gate failure does not suppress the shared client (and vice versa) — the
// same independent-failure behaviour the pre-extraction main.go had.
type EmbedClients struct {
	Gate      *kitembed.Client
	GateErr   error
	Shared    *kitembed.Client
	SharedErr error
}

// NewEmbedClients constructs the two embed clients for one embed server URL
// from a single call, so a future edit cannot give one client the other's
// options. baseOpts select the backend, dimension, and logger and are applied
// to BOTH clients; the budget opts (EmbedClientBudgetOpts) are appended ONLY
// to the gate client (inside NewEmbedClient). The shared client is built with
// kitembed.NewClient and the base opts alone, preserving kitembed's library
// defaults.
//
// This is the production construction site extracted from main.go:initEngine
// so the two-client split is testable: TestRelevanceEmbedBudget_WiringSplit
// asserts both clients come from this call with the correct, distinct budgets.
func NewEmbedClients(url string, baseOpts ...kitembed.Opt) EmbedClients {
	gate, gateErr := NewEmbedClient(url, baseOpts...)
	shared, sharedErr := kitembed.NewClient(url, baseOpts...)
	return EmbedClients{
		Gate:      gate,
		GateErr:   gateErr,
		Shared:    shared,
		SharedErr: sharedErr,
	}
}
