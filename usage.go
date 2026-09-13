package snapnedit

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

// UsageGroupBy is the dimension GET /usage buckets its series along — one at a
// time.
type UsageGroupBy string

// The groupings. [GroupByDay] is zero-filled across the whole range and
// ordered oldest first, so a chart has a point per day; every other grouping
// comes back busiest first, the way a table reads.
const (
	GroupByDay       UsageGroupBy = "day"
	GroupByKey       UsageGroupBy = "key"
	GroupByOrigin    UsageGroupBy = "origin"
	GroupByOperation UsageGroupBy = "operation"
	GroupBySource    UsageGroupBy = "source"
)

// UsageSource is how a job was requested, as the API persists it on the job
// row.
type UsageSource string

// The job sources. SourceSession (a signed-in website visitor) and
// SourceAnonymous (no credential at all) are never billed, and an anonymous
// job is never attributed to an account — filtering by it yields nothing.
const (
	SourceAPI       UsageSource = "api"
	SourceEmbed     UsageSource = "embed"
	SourceSession   UsageSource = "session"
	SourceAnonymous UsageSource = "anonymous"
)

// APIKeyKind is the flavour of an API key. Only a publishable key carries a
// daily credit cap.
type APIKeyKind string

// The API key kinds.
const (
	KeySecret      APIKeyKind = "secret"
	KeyPublishable APIKeyKind = "publishable"
)

// UsageUnattributed is the series key for a bucket with no value for the
// grouped dimension — a website job has no API key, an sk_ job has no origin.
const UsageUnattributed = "none"

// UsageQuery narrows [Client.GetUsage]. Every field is optional: a nil query
// (or a zero UsageQuery) is the last 30 days grouped by day.
type UsageQuery struct {
	// From is the inclusive start of the window. Zero means 30 days before To.
	//
	// It is sent as an INSTANT, so to cover a whole UTC day name its first
	// millisecond here and its last in To.
	From time.Time
	// To is the inclusive end of the window. Zero means now. A window wider
	// than 366 days is refused with [CodeInvalidInput].
	To time.Time
	// GroupBy selects the series dimension. Empty means [GroupByDay].
	GroupBy UsageGroupBy
	// KeyID restricts the report to jobs authenticated with this API key. It is
	// ignored for an embed token, which is already scoped to its own.
	KeyID string
	// Origin restricts the report to embed jobs from this host surface —
	// "https://app.example.com" or "native:com.acme.photos".
	Origin string
	// Operation restricts the report to one operation.
	Operation Operation
	// Source restricts the report to jobs that arrived this way.
	Source UsageSource
}

// values renders the query string. Zero-valued fields are omitted entirely
// rather than sent empty, which the API's strict schema would reject.
func (q *UsageQuery) values() url.Values {
	out := url.Values{}
	if q == nil {
		return out
	}
	if !q.From.IsZero() {
		out.Set("from", usageInstant(q.From))
	}
	if !q.To.IsZero() {
		out.Set("to", usageInstant(q.To))
	}
	for name, value := range map[string]string{
		"groupBy":   string(q.GroupBy),
		"keyId":     q.KeyID,
		"origin":    q.Origin,
		"operation": string(q.Operation),
		"source":    string(q.Source),
	} {
		if value != "" {
			out.Set(name, value)
		}
	}
	return out
}

// usageInstant renders a range end as a UTC RFC 3339 instant with millisecond
// precision — the same spelling the API echoes back in [UsageRange].
func usageInstant(at time.Time) string {
	return at.UTC().Format("2006-01-02T15:04:05.000Z")
}

// UsageRange is the window the report actually covers.
//
// Both ends are ISO 8601 INSTANTS, not the YYYY-MM-DD a query may name: a bare
// to=2026-09-13 comes back as the last millisecond of that UTC day.
type UsageRange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// UsageFacts are the seven counters every usage bucket carries.
//
// Jobs counts REQUESTS — a cache hit is a job too — while Credits is what was
// actually debited, so a free operation, a cache hit and a website job each
// contribute 0.
type UsageFacts struct {
	// Jobs is jobs created, cache hits and delivery-only clones included.
	Jobs int `json:"jobs"`
	// Credits is what was actually debited.
	Credits int `json:"credits"`
	// CacheHits is jobs served from the result cache rather than by a provider.
	CacheHits int `json:"cacheHits"`
	// Free is jobs for an operation that costs nothing.
	Free int `json:"free"`
	// Failed is jobs that ended in [JobFailed].
	Failed int `json:"failed"`
	// Delivered is jobs whose result was successfully PUT to a destination.
	Delivered int `json:"delivered"`
	// DeliveryFailed is jobs whose delivery failed. The job itself may still
	// have succeeded — a failed delivery never fails a job.
	DeliveryFailed int `json:"deliveryFailed"`
}

// UsageTotals is the whole requested range, undivided.
type UsageTotals struct {
	UsageFacts
	// Sessions is embedded-editor sessions STARTED in the range.
	Sessions int `json:"sessions"`
	// ActiveSessions is sessions last seen within the range, whenever they
	// started.
	ActiveSessions int `json:"activeSessions"`
}

// UsageSeriesPoint is one bucket of the series: a day, an API key, an embed
// origin, an operation or a source, per the query's [UsageGroupBy].
type UsageSeriesPoint struct {
	// Key is the dimension value — a YYYY-MM-DD, a key id, an origin, an
	// operation id or a source — or [UsageUnattributed] when the rows have none.
	Key string `json:"key"`
	// Label is what to show a human: the API key's NAME when grouping by key,
	// otherwise the key itself.
	Label string `json:"label"`
	UsageFacts
	// Sessions is embedded-editor sessions in this bucket. Always 0 when
	// grouping by operation or source, which sessions have no dimension for.
	Sessions int `json:"sessions"`
}

// UsageKeyRow is one of the account's live API keys, with today's spend against
// its cap.
type UsageKeyRow struct {
	ID   string     `json:"id"`
	Name string     `json:"name"`
	Kind APIKeyKind `json:"kind"`
	// DailyCreditLimit is the per-UTC-day ceiling on a publishable key, or nil
	// when the key is uncapped.
	DailyCreditLimit *int `json:"dailyCreditLimit"`
	// UsedToday is the credits this key has spent so far TODAY (UTC), whatever
	// range was requested.
	UsedToday int `json:"usedToday"`
}

// UsageReport is what GET /usage returns.
type UsageReport struct {
	// Range is the window the API resolved the query to.
	Range UsageRange `json:"range"`
	// GroupBy echoes the dimension Series is bucketed along.
	GroupBy UsageGroupBy `json:"groupBy"`
	// Totals is the whole range, undivided.
	Totals UsageTotals `json:"totals"`
	// Series is one bucket per value of the grouped dimension.
	Series []UsageSeriesPoint `json:"series"`
	// Keys lists the account's live API keys with today's spend. It is empty
	// for an embed token: the API omits the roster for a credential that may
	// not enumerate the account's other keys.
	Keys []UsageKeyRow `json:"keys"`
}

// Point returns the series bucket with this key, or nil when the range has
// none.
func (r *UsageReport) Point(key string) *UsageSeriesPoint {
	for i := range r.Series {
		if r.Series[i].Key == key {
			return &r.Series[i]
		}
	}
	return nil
}

// GetUsage reports what the account has actually spent — jobs, credits, cache
// hits, deliveries and embedded-editor sessions — bucketed along one dimension
// at a time. GET /usage.
//
//	report, err := client.GetUsage(ctx, &snapnedit.UsageQuery{
//		From:    time.Now().AddDate(0, 0, -7),
//		GroupBy: snapnedit.GroupByOperation,
//	})
//
// Pass a nil query for the last 30 days grouped by day.
//
// Readable with a secret key or an embed token. An embed token is scoped to
// its own key: [UsageQuery.KeyID] is ignored for it and Keys comes back empty.
func (c *Client) GetUsage(ctx context.Context, query *UsageQuery) (*UsageReport, error) {
	if err := c.requireKey("reading usage"); err != nil {
		return nil, err
	}
	path := "/usage"
	if encoded := query.values().Encode(); encoded != "" {
		path += "?" + encoded
	}
	var report UsageReport
	if err := c.doJSON(ctx, request{
		method: http.MethodGet,
		path:   path,
		auth:   true,
	}, &report); err != nil {
		return nil, err
	}
	return &report, nil
}
