package ops

import "goodkind.io/tack/internal/clispec"

// authTokenCreateResult is what token-create reports. In a dry run it names
// the holder and org the token would be issued to; after an execute it also
// carries the token's id and its raw value.
type authTokenCreateResult struct {
	clispec.ResultMarker
	Command   string `json:"command"`
	DryRun    bool   `json:"dry_run"`
	UserID    string `json:"user_id"`
	Email     string `json:"email"`
	Label     string `json:"label"`
	OrgID     string `json:"org_id"`
	TokenID   string `json:"token_id,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	// Token is the raw bearer. It appears here once and nowhere else: not in
	// a log, not in the ledger, not in the database.
	Token string `json:"token,omitempty"`
}

// authTokenListEntry is one token as token-list reports it. Raw values are
// never stored, so none is here.
type authTokenListEntry struct {
	TokenID   string `json:"token_id"`
	Label     string `json:"label"`
	CreatedAt string `json:"created_at"`
	LastUsed  string `json:"last_used,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

type authTokenListResult struct {
	clispec.ResultMarker
	Command string               `json:"command"`
	UserID  string               `json:"user_id"`
	Email   string               `json:"email"`
	Tokens  []authTokenListEntry `json:"tokens"`
}

// authTokenRevokeResult is what token-revoke reports: the token it removed,
// or in a dry run the token it would remove.
type authTokenRevokeResult struct {
	clispec.ResultMarker
	Command string `json:"command"`
	DryRun  bool   `json:"dry_run"`
	TokenID string `json:"token_id"`
	UserID  string `json:"user_id"`
	Email   string `json:"email"`
	Label   string `json:"label"`
	OrgID   string `json:"org_id"`
}
