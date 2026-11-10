package ops

import "goodkind.io/tack/internal/clispec"

// authTokenCreateResult reports the user and organization for token-create. A
// dry run reports the intended recipient. Execution also reports the token ID.
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
	// Token contains the raw bearer for the execution report's one-time display.
	// Logs, the ledger, and the database must not record this value.
	Token string `json:"token,omitempty"`
}

// authTokenListEntry reports one token without its raw bearer value.
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

// authTokenRevokeResult reports the token that token-revoke removed or would
// remove in a dry run.
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
