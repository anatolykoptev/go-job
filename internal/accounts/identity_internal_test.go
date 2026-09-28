package accounts

// Deny-matrix for the pure seam behind AccountFrom — accountFromIdentity is
// exercised directly because the ctx keys stamping *Session / *TokenInfo are
// unexported in both SDKs (the end-to-end ctx extraction is proven in
// accounts_test via the real middlewares).

import (
	"testing"

	panelauth "github.com/anatolykoptev/go-panel/auth"
	"github.com/google/uuid"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/stretchr/testify/require"
)

func TestAccountFromIdentity_DenyMatrix(t *testing.T) {
	good := uuid.New()

	cases := []struct {
		name string
		sess *panelauth.Session
		ti   *sdkauth.TokenInfo
		want uuid.UUID
		ok   bool
	}{
		{name: "no identity at all", sess: nil, ti: nil, want: uuid.Nil, ok: false},
		{name: "empty ctx pieces", sess: nil, ti: nil, ok: false},
		{name: "session valid uuid", sess: &panelauth.Session{UserID: good.String()}, want: good, ok: true},
		{name: "session empty UserID", sess: &panelauth.Session{UserID: ""}, ok: false},
		{name: "session malformed UserID", sess: &panelauth.Session{UserID: "not-a-uuid"}, ok: false},
		{name: "session nil-uuid UserID", sess: &panelauth.Session{UserID: uuid.Nil.String()}, ok: false},
		{name: "tokeninfo valid uuid", ti: &sdkauth.TokenInfo{UserID: good.String()}, want: good, ok: true},
		{name: "tokeninfo empty UserID", ti: &sdkauth.TokenInfo{UserID: ""}, ok: false},
		{name: "tokeninfo malformed UserID", ti: &sdkauth.TokenInfo{UserID: "guest1"}, ok: false},
		{name: "tokeninfo nil-uuid UserID", ti: &sdkauth.TokenInfo{UserID: uuid.Nil.String()}, ok: false},
		{
			name: "session wins over tokeninfo (paths are exclusive in prod; deterministic precedence)",
			sess: &panelauth.Session{UserID: good.String()},
			ti:   &sdkauth.TokenInfo{UserID: uuid.New().String()},
			want: good, ok: true,
		},
		{
			name: "broken session denies — never falls through to a valid token",
			sess: &panelauth.Session{UserID: "garbage"},
			ti:   &sdkauth.TokenInfo{UserID: good.String()},
			want: uuid.Nil, ok: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := accountFromIdentity(tc.sess, tc.ti)
			require.Equal(t, tc.ok, ok)
			require.Equal(t, tc.want, got)
		})
	}
}
