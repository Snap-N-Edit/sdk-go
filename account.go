package snapnedit

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// KeyMode says whether an API key spends real credits.
type KeyMode string

// The key modes. A [KeyModeTest] key (sk_test_…) runs jobs for free on
// placeholder results, never touching the result cache.
const (
	KeyModeLive KeyMode = "live"
	KeyModeTest KeyMode = "test"
)

// AccountKey is the API key a request authenticated with.
type AccountKey struct {
	ID   string  `json:"id"`
	Mode KeyMode `json:"mode"`
}

// Account is what GET /account returns: who a secret key belongs to, its
// balance, and the key's mode.
type Account struct {
	AccountID string `json:"accountId"`
	// Email is the account's address, or nil when it has none.
	Email *string `json:"email"`
	Plan  string  `json:"plan"`
	// Credits is the live credit balance.
	Credits int        `json:"credits"`
	Key     AccountKey `json:"key"`
}

// GetAccount reads the account the client's key belongs to — GET /account.
//
// Secret keys only: an embed token or a publishable key is [CodeUnauthorized].
func (c *Client) GetAccount(ctx context.Context) (*Account, error) {
	if err := c.requireKey("reading the account"); err != nil {
		return nil, err
	}
	var out Account
	if err := c.doJSON(ctx, request{method: http.MethodGet, path: "/account", auth: true}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// JobPriority is the lane a job runs in.
type JobPriority string

// The job priorities.
const (
	PriorityInteractive JobPriority = "interactive"
	PriorityBackground  JobPriority = "background"
)

// ListJobsQuery narrows [Client.ListJobs]. Every field is optional.
type ListJobsQuery struct {
	// Limit is 1-100. Zero means 20.
	Limit int
	// State keeps only jobs in this state.
	State JobState
	// Priority keeps only jobs in this lane.
	Priority JobPriority
}

// JobListItem is one row of [Client.ListJobs]: a [Job] plus its id and
// creation time.
type JobListItem struct {
	Job
	JobID string `json:"jobId"`
	// CreatedAt is an RFC 3339 timestamp.
	CreatedAt string `json:"createdAt"`
}

// ListJobs returns the account's newest jobs first — GET /jobs?limit=N.
// Pass a nil query for the newest 20.
//
// Secret keys only. A Limit outside 1-100 is [CodeInvalidInput].
func (c *Client) ListJobs(ctx context.Context, query *ListJobsQuery) ([]JobListItem, error) {
	if err := c.requireKey("listing jobs"); err != nil {
		return nil, err
	}
	if query == nil {
		query = &ListJobsQuery{}
	}
	limit := query.Limit
	if limit == 0 {
		limit = 20
	}
	values := url.Values{"limit": {strconv.Itoa(limit)}}
	if query.State != "" {
		values.Set("status", string(query.State))
	}
	if query.Priority != "" {
		values.Set("priority", string(query.Priority))
	}
	var out struct {
		Jobs []JobListItem `json:"jobs"`
	}
	if err := c.doJSON(ctx, request{method: http.MethodGet, path: "/jobs?" + values.Encode(), auth: true}, &out); err != nil {
		return nil, err
	}
	return out.Jobs, nil
}
