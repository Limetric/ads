package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	mathrand "math/rand/v2"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// BingClient talks to the Microsoft Advertising (Bing Ads) API v13 over REST.
// It is safe for concurrent use.
//
// This is the JSON/REST surface, not SOAP: operations are POSTs (or a PUT to
// update) to per-service hosts, with PascalCase request and response bodies.
// Polymorphic objects carry a "Type" discriminator naming the derived type —
// a report request is a CampaignPerformanceReportRequest, never a bare
// ReportRequest.
type BingClient struct {
	cfg    *BingConfig
	http   *http.Client
	tokens oauth2.TokenSource
}

// bingAPIVersion is the API version this client targets. It is the single place
// the version is spelled; every service URL derives from it.
const bingAPIVersion = "v13"

// bingService is one of Microsoft's per-service hosts. Unlike Google, where one
// host serves everything, each Bing service answers on its own subdomain and
// its own first URL segment — and sandbox is a different host again.
type bingService struct {
	// Label names the service in error messages.
	Label string
	// Host is the subdomain: campaign, clientcenter, reporting.
	Host string
	// Path is the first URL segment: CampaignManagement, CustomerManagement, Reporting.
	Path string
}

var (
	bingCampaignService = bingService{Label: "campaign management", Host: "campaign", Path: "CampaignManagement"}
	bingCustomerService = bingService{Label: "customer management", Host: "clientcenter", Path: "CustomerManagement"}
	bingReportService   = bingService{Label: "reporting", Host: "reporting", Path: "Reporting"}
)

// url builds the endpoint for one operation. A configured base URL replaces the
// host but keeps the service path, so a single httptest server can stand in for
// every service and still route by URL — which is what makes these calls
// testable offline (see AGENTS.md).
func (s bingService) url(cfg *BingConfig, operation string) string {
	base := cfg.BaseURL
	if base == "" {
		host := s.Host + ".api.bingads.microsoft.com"
		if cfg.Environment == bingEnvSandbox {
			host = s.Host + ".api.sandbox.bingads.microsoft.com"
		}
		base = "https://" + host
	}
	return fmt.Sprintf("%s/%s/%s/%s", base, s.Path, bingAPIVersion, operation)
}

// NewBingClient builds a client from config, resolving the saved sign-in and
// validating credentials first — resolution comes first because it is what
// decides whether there is a sign-in at all.
func NewBingClient(ctx context.Context, cfg *BingConfig) (*BingClient, error) {
	refreshToken, err := cfg.resolveRefreshToken()
	if err != nil {
		return nil, err
	}
	if err := cfg.validate(refreshToken); err != nil {
		return nil, err
	}
	return &BingClient{
		cfg: cfg,
		// Reports are submitted and polled with this client; the download is a
		// separate, longer-lived transfer (see DownloadReport).
		http:   &http.Client{Timeout: 60 * time.Second},
		tokens: newTokenSource(ctx, cfg.oauth(refreshToken)),
	}, nil
}

// resolveAccountID returns the ad account a call should act on: the explicit
// argument when given, otherwise the configured default. It mirrors Google's
// resolveCustomerID, including the rule that a non-blank value which normalizes
// to nothing is an error rather than a silent fallback — a write must never be
// redirected to a different account by a typo.
func (c *BingClient) resolveAccountID(explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		id := normalizeBingID(explicit)
		if !validBingID(id) {
			return "", fmt.Errorf("invalid bing account ID %q — expected digits, e.g. 123456789", explicit)
		}
		return id, nil
	}
	if c != nil && c.cfg != nil && c.cfg.DefaultAccountID != "" {
		return c.cfg.DefaultAccountID, nil
	}
	return "", fmt.Errorf("account_id is required — pass --account-id, or set a default with `ads config bing set-account <id>` (or BING_ADS_ACCOUNT_ID)")
}

// buildHeaders sets the four headers every Bing Ads REST call needs: the OAuth
// bearer token, the developer token, the manager account (CustomerId), and the
// ad account the entities belong to (CustomerAccountId).
//
// Microsoft's manager/account split lines up with Google's login-customer /
// customer split: CustomerId is the manager the user operates from, and
// CustomerAccountId is the account being acted on.
func (c *BingClient) buildHeaders(req *http.Request, accountID string) error {
	tok, err := c.tokens.Token()
	if err != nil {
		return fmt.Errorf("obtain access token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	req.Header.Set("Content-Type", "application/json")

	dev := c.cfg.DeveloperToken
	if dev == "" && c.cfg.isTest() {
		dev = "test-developer-token"
	}
	req.Header.Set("DeveloperToken", dev)
	if c.cfg.CustomerID != "" {
		req.Header.Set("CustomerId", c.cfg.CustomerID)
	}
	if accountID != "" {
		req.Header.Set("CustomerAccountId", accountID)
	}
	return nil
}

// Throttle codes. Microsoft's limits are per-user, per-minute, and unpublished
// ("the details of the service limits are internal and subject to change"), so
// the only workable client behaviour is to recognize the codes and back off.
const (
	bingErrCallRateExceeded        = 117  // CallRateExceeded — resubmit after ~60s
	bingErrConcurrentRequestLimit  = 207  // ConcurrentRequestOverLimit — too many reports in flight
	bingErrBulkNoMoreCallsForNow   = 4204 // BulkServiceNoMoreCallsPermittedForTheTimePeriod — up to 15 minutes
	bingThrottleRetryMaxAttempts   = 3
	bingUnthrottledRetryMaxAttempt = retryMaxAttempts
)

// bingThrottleAdvice returns what the user can actually do about a throttle
// code, since none of them clear by retrying immediately.
func bingThrottleAdvice(code int) string {
	switch code {
	case bingErrCallRateExceeded:
		return "the per-minute call limit for this user was exceeded — wait 60 seconds and try again"
	case bingErrConcurrentRequestLimit:
		return "too many report requests are already in flight — wait for the running ones to finish (`ads bing report fetch <job>`) before submitting another"
	case bingErrBulkNoMoreCallsForNow:
		return "the per-period call limit for this account was reached — wait up to 15 minutes and try again"
	default:
		return "the service is rate limiting this user — wait a minute and try again"
	}
}

// bingThrottleBaseDelay is a var so tests can shrink it.
var bingThrottleBaseDelay = 2 * time.Second

// bingThrottleDelay is the wait before re-attempting a throttled call. It is
// deliberately shorter than the limits themselves: a tool call cannot sit for a
// minute, so the retries only catch a limit that has just rolled over, and the
// error that survives them says how long to really wait.
func bingThrottleDelay(attempt int) time.Duration {
	d := bingThrottleBaseDelay << (attempt - 1)
	return d + time.Duration(mathrand.Int64N(int64(d)/2+1))
}

// call issues one JSON request to a service operation and decodes the response
// into out, retrying transient failures per the policy.
func (c *BingClient) call(ctx context.Context, svc bingService, method, operation, accountID string, body, out any, policy retryPolicy) error {
	payload, err := encodeJSON(body)
	if err != nil {
		return err
	}
	url := svc.url(c.cfg, operation)
	for attempt := 1; ; attempt++ {
		var reader io.Reader
		if payload != nil {
			reader = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, reader)
		if err != nil {
			return err
		}
		if err := c.buildHeaders(req, accountID); err != nil {
			return err
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("call %s %s: %w", svc.Label, operation, err)
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("read %s %s response: %w", svc.Label, operation, err)
		}
		if resp.StatusCode >= 300 {
			apiErr := bingError(resp.StatusCode, svc, operation, data)
			if delay, ok := bingRetryDelay(attempt, apiErr, resp, policy); ok {
				select {
				case <-time.After(delay):
					continue
				case <-ctx.Done():
					return apiErr
				}
			}
			return apiErr
		}
		if out != nil && len(data) > 0 {
			if err := json.Unmarshal(data, out); err != nil {
				return fmt.Errorf("decode %s %s response: %w", svc.Label, operation, err)
			}
		}
		return nil
	}
}

// bingRetryDelay decides whether a failed attempt is worth repeating, and how
// long to wait first. Throttling gets its own budget and its own backoff: the
// codes arrive with assorted HTTP statuses, so the status alone cannot classify
// them, and a throttle is exactly the case where retrying too fast is what
// caused the problem.
func bingRetryDelay(attempt int, err *bingAPIError, resp *http.Response, policy retryPolicy) (time.Duration, bool) {
	if err.throttled() {
		if attempt >= bingThrottleRetryMaxAttempts {
			return 0, false
		}
		return bingThrottleDelay(attempt), true
	}
	if attempt >= bingUnthrottledRetryMaxAttempt || !policy.retryable(resp.StatusCode) {
		return 0, false
	}
	return backoffDelay(attempt, resp.Header.Get("Retry-After")), true
}

// post issues a read-only POST (Bing's queries are POSTs) with the full retry
// policy. Mutating callers use put, which does not retry 5xx.
func (c *BingClient) post(ctx context.Context, svc bingService, operation, accountID string, body, out any) error {
	return c.call(ctx, svc, http.MethodPost, operation, accountID, body, out, retryReads)
}

// postWrite is post for an operation that changes something: a 5xx may mean the
// change was applied, so only rate limiting is retried.
func (c *BingClient) postWrite(ctx context.Context, svc bingService, operation, accountID string, body, out any) error {
	return c.call(ctx, svc, http.MethodPost, operation, accountID, body, out, retryWrites)
}

// put issues a mutating PUT — Bing's update operations use it.
func (c *BingClient) put(ctx context.Context, svc bingService, operation, accountID string, body, out any) error {
	return c.call(ctx, svc, http.MethodPut, operation, accountID, body, out, retryWrites)
}

// --- errors ---------------------------------------------------------------

// bingErrorItem is one entry of a Bing Ads fault. The same shape covers an
// operation error (the whole call failed) and a batch error (one item in a
// batch failed), which is why Index and FieldPath are optional.
type bingErrorItem struct {
	Code      int    `json:"Code"`
	ErrorCode string `json:"ErrorCode"`
	Message   string `json:"Message"`
	Details   string `json:"Details"`
	// Index identifies which item in a batch this error belongs to. Bing does
	// not return a null entry per successful item, so the index is the only
	// link back to the request (see bingPartialErrors).
	Index     *int   `json:"Index,omitempty"`
	FieldPath string `json:"FieldPath,omitempty"`
}

// describe renders one error the way the user needs to read it: the symbolic
// code (which is what the documentation is indexed by), the numeric code, and
// the message.
func (e bingErrorItem) describe() string {
	var b strings.Builder
	switch {
	case e.ErrorCode != "" && e.Code != 0:
		fmt.Fprintf(&b, "%s (%d)", e.ErrorCode, e.Code)
	case e.ErrorCode != "":
		b.WriteString(e.ErrorCode)
	default:
		fmt.Fprintf(&b, "code %d", e.Code)
	}
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	if e.Details != "" {
		fmt.Fprintf(&b, " [%s]", e.Details)
	}
	if e.FieldPath != "" {
		fmt.Fprintf(&b, " (field %s)", e.FieldPath)
	}
	return b.String()
}

// bingAPIError is a non-2xx Bing Ads response. It carries the HTTP status so
// doctor can tell a definitive rejection from a transient one, and the parsed
// error items so the message names the code the documentation uses.
type bingAPIError struct {
	status    int
	service   string
	operation string
	items     []bingErrorItem
	raw       string
}

func (e *bingAPIError) Error() string {
	msg := fmt.Sprintf("bing ads %s API %d (%s)", e.service, e.status, e.operation)
	if len(e.items) == 0 {
		return msg + ": " + e.raw
	}
	parts := make([]string, 0, len(e.items))
	for _, item := range e.items {
		parts = append(parts, item.describe())
	}
	msg += ": " + strings.Join(parts, " | ")
	if e.throttled() {
		msg += " — " + bingThrottleAdvice(e.throttleCode())
	}
	return msg
}

// isClientError implements definitiveAPIError for doctor. A throttle is
// deliberately excluded: it arrives as a 4xx but says nothing about whether the
// setup works, and reporting it as a broken setup would be wrong.
func (e *bingAPIError) isClientError() bool {
	if e.throttled() {
		return false
	}
	return e.status >= 400 && e.status < 500
}

// throttled reports whether this failure is Microsoft rate limiting us.
func (e *bingAPIError) throttled() bool { return e.throttleCode() != 0 }

func (e *bingAPIError) throttleCode() int {
	for _, item := range e.items {
		switch item.Code {
		case bingErrCallRateExceeded, bingErrConcurrentRequestLimit, bingErrBulkNoMoreCallsForNow:
			return item.Code
		}
	}
	return 0
}

// bingError turns a non-2xx response into a readable error.
//
// The fault payload is not one shape: an operation-level failure arrives under
// "OperationErrors" or "Errors" depending on the service and fault type, and a
// batch failure under "BatchErrors". All of them are lists of the same item, so
// all of them are read.
func bingError(status int, svc bingService, operation string, body []byte) *bingAPIError {
	var payload struct {
		Errors          []bingErrorItem `json:"Errors"`
		OperationErrors []bingErrorItem `json:"OperationErrors"`
		BatchErrors     []bingErrorItem `json:"BatchErrors"`
		Message         string          `json:"Message"`
	}
	e := &bingAPIError{status: status, service: svc.Label, operation: operation, raw: strings.TrimSpace(string(body))}
	if json.Unmarshal(body, &payload) == nil {
		e.items = append(e.items, payload.Errors...)
		e.items = append(e.items, payload.OperationErrors...)
		e.items = append(e.items, payload.BatchErrors...)
		if len(e.items) == 0 && payload.Message != "" {
			e.items = []bingErrorItem{{Message: payload.Message}}
		}
	}
	if e.raw == "" {
		e.raw = http.StatusText(status)
	}
	return e
}

// bingPartialErrors turns a mutate response's PartialErrors into an error that
// says which items failed.
//
// Bing reports partial success per item and returns nothing at all for the ones
// that worked, so a caller that ignores this list reports "ok" for a batch that
// was half rejected. Every write path checks it (see the issue's "never a bare
// ok").
func bingPartialErrors(errs []bingErrorItem, total int) error {
	if len(errs) == 0 {
		return nil
	}
	parts := make([]string, 0, len(errs))
	for _, e := range errs {
		if e.Index != nil {
			parts = append(parts, fmt.Sprintf("item %d — %s", *e.Index, e.describe()))
			continue
		}
		parts = append(parts, e.describe())
	}
	applied := total - len(errs)
	if applied < 0 {
		applied = 0
	}
	return fmt.Errorf("%d of %d item(s) applied; %d failed: %s", applied, total, len(errs), strings.Join(parts, " | "))
}

// --- customer management --------------------------------------------------

// BingAccountInfo is one account as GetAccountsInfo returns it.
type BingAccountInfo struct {
	ID                     string `json:"Id"`
	Name                   string `json:"Name"`
	Number                 string `json:"Number"`
	AccountLifeCycleStatus string `json:"AccountLifeCycleStatus"`
	PauseReason            string `json:"PauseReason,omitempty"`
}

// ListAccounts returns the ad accounts reachable from the configured manager
// account. With no manager account set, Microsoft resolves the customer from
// the signed-in user's credentials, so this works either way.
func (c *BingClient) ListAccounts(ctx context.Context, onlyParentAccounts bool) ([]BingAccountInfo, error) {
	body := map[string]any{"OnlyParentAccounts": onlyParentAccounts}
	if c.cfg.CustomerID != "" {
		body["CustomerId"] = c.cfg.CustomerID
	}
	var out struct {
		AccountsInfo []BingAccountInfo `json:"AccountsInfo"`
	}
	if err := c.post(ctx, bingCustomerService, "AccountsInfo/Query", "", body, &out); err != nil {
		return nil, err
	}
	return out.AccountsInfo, nil
}

// BingAccount is the detail GetAccount returns for one ad account. Only the
// fields ads reports are decoded; the payload carries billing and tax data that
// no tool here should be handing to an agent.
type BingAccount struct {
	ID                     string `json:"Id"`
	Name                   string `json:"Name"`
	Number                 string `json:"Number"`
	CurrencyCode           string `json:"CurrencyCode"`
	TimeZone               string `json:"TimeZone"`
	Language               string `json:"Language"`
	AccountLifeCycleStatus string `json:"AccountLifeCycleStatus"`
	PauseReason            string `json:"PauseReason,omitempty"`
	ParentCustomerID       string `json:"ParentCustomerId"`
	AccountMode            string `json:"AccountMode"`
}

// GetAccount returns one ad account's details, including the currency every
// cost figure in this account's reports is denominated in.
func (c *BingClient) GetAccount(ctx context.Context, accountID string) (*BingAccount, error) {
	var out struct {
		Account BingAccount `json:"Account"`
	}
	body := map[string]any{"AccountId": accountID}
	if err := c.post(ctx, bingCustomerService, "Account/Query", accountID, body, &out); err != nil {
		return nil, err
	}
	return &out.Account, nil
}

// --- campaign management --------------------------------------------------

// bingAllCampaignTypes is the CampaignType filter ads asks for: every value in
// the v13 CampaignType set, as space-delimited flags rather than a list.
//
// The element is documented as optional with a default of *Search*, so leaving
// it out — or leaving a value out of it — silently hides those campaigns.
// Anything missing here is invisible twice over: absent from `bing_campaigns`
// with a total_count to match, and absent from the list GetCampaign filters, so
// a budget write against a campaign the user is looking at in the Microsoft UI
// fails with "campaign not found".
const bingAllCampaignTypes = "Search Shopping DynamicSearchAds Audience Hotel PerformanceMax App"

// BingCampaign is a campaign as the REST API returns it. Money fields are plain
// currency amounts, not micros.
type BingCampaign struct {
	ID           string   `json:"Id"`
	Name         string   `json:"Name"`
	Status       string   `json:"Status"`
	CampaignType string   `json:"CampaignType"`
	SubType      string   `json:"SubType,omitempty"`
	DailyBudget  *float64 `json:"DailyBudget"`
	BudgetType   string   `json:"BudgetType"`
	// BudgetID references the shared Budget this campaign draws on. Read it
	// through sharedBudgetID: the field is meaningful by value, not by presence.
	BudgetID *string `json:"BudgetId"`
	TimeZone string  `json:"TimeZone,omitempty"`
	// BidStrategyID references a portfolio bid strategy, with the same
	// value semantics as BudgetID. Read it through portfolioBidStrategyID.
	BidStrategyID *string `json:"BidStrategyId,omitempty"`
}

// bingOptionalID reads one of Microsoft's "reference or nothing" identifier
// fields.
//
// Both BudgetId and BidStrategyId are documented the same way: a value that is
// not null *and greater than zero* means the campaign uses the shared entity,
// and zero is the value you write to detach it. So a campaign that was moved
// off a shared budget comes back carrying "0", and reading presence instead of
// value would report it as still shared — which, for BudgetId, blocks the only
// Bing write tool there is.
func bingOptionalID(id *string) string {
	if id == nil {
		return ""
	}
	switch v := strings.TrimSpace(*id); v {
	case "", "0":
		return ""
	default:
		return v
	}
}

// sharedBudgetID is the shared Budget this campaign draws on, or "" when it
// uses its own DailyBudget.
func (c *BingCampaign) sharedBudgetID() string { return bingOptionalID(c.BudgetID) }

// portfolioBidStrategyID is the portfolio bid strategy this campaign shares, or
// "" when it uses its own.
func (c *BingCampaign) portfolioBidStrategyID() string { return bingOptionalID(c.BidStrategyID) }

// ListCampaigns returns every campaign in an account.
func (c *BingClient) ListCampaigns(ctx context.Context, accountID string) ([]BingCampaign, error) {
	body := map[string]any{"AccountId": accountID, "CampaignType": bingAllCampaignTypes}
	var out struct {
		Campaigns []BingCampaign `json:"Campaigns"`
	}
	if err := c.post(ctx, bingCampaignService, "Campaigns/QueryByAccountId", accountID, body, &out); err != nil {
		return nil, err
	}
	return out.Campaigns, nil
}

// GetCampaign returns one campaign by ID, or nil when the account has no such
// campaign. It filters the account's campaigns client-side rather than calling
// a by-ids endpoint, which keeps the whole client on operations whose behaviour
// with the CampaignType flag is known.
func (c *BingClient) GetCampaign(ctx context.Context, accountID, campaignID string) (*BingCampaign, error) {
	campaigns, err := c.ListCampaigns(ctx, accountID)
	if err != nil {
		return nil, err
	}
	for i := range campaigns {
		if campaigns[i].ID == campaignID {
			return &campaigns[i], nil
		}
	}
	return nil, nil
}

// BingAdGroup is an ad group as the REST API returns it.
type BingAdGroup struct {
	ID          string    `json:"Id"`
	Name        string    `json:"Name"`
	Status      string    `json:"Status"`
	AdGroupType string    `json:"AdGroupType,omitempty"`
	Language    string    `json:"Language,omitempty"`
	Network     string    `json:"Network,omitempty"`
	CpcBid      *bingBid  `json:"CpcBid"`
	StartDate   *bingDate `json:"StartDate"`
	EndDate     *bingDate `json:"EndDate"`
}

// bingBid is Bing's money wrapper: every bid and rate arrives as {"Amount": n}.
type bingBid struct {
	Amount *float64 `json:"Amount"`
}

func (b *bingBid) value() *float64 {
	if b == nil {
		return nil
	}
	return b.Amount
}

// bingDate is Bing's date shape — separate integer fields, no time zone.
type bingDate struct {
	Day   int `json:"Day"`
	Month int `json:"Month"`
	Year  int `json:"Year"`
}

// String renders a Bing date as ISO-8601, or "" when it is not set.
func (d *bingDate) String() string {
	if d == nil || d.Year == 0 {
		return ""
	}
	return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day)
}

// ListAdGroups returns every ad group in a campaign.
func (c *BingClient) ListAdGroups(ctx context.Context, accountID, campaignID string) ([]BingAdGroup, error) {
	body := map[string]any{"CampaignId": campaignID}
	var out struct {
		AdGroups []BingAdGroup `json:"AdGroups"`
	}
	if err := c.post(ctx, bingCampaignService, "AdGroups/QueryByCampaignId", accountID, body, &out); err != nil {
		return nil, err
	}
	return out.AdGroups, nil
}

// BingKeyword is a keyword as the REST API returns it.
type BingKeyword struct {
	ID              string   `json:"Id"`
	Text            string   `json:"Text"`
	MatchType       string   `json:"MatchType"`
	Status          string   `json:"Status"`
	EditorialStatus string   `json:"EditorialStatus,omitempty"`
	Bid             *bingBid `json:"Bid"`
}

// ListKeywords returns every keyword in an ad group.
func (c *BingClient) ListKeywords(ctx context.Context, accountID, adGroupID string) ([]BingKeyword, error) {
	body := map[string]any{"AdGroupId": adGroupID}
	var out struct {
		Keywords []BingKeyword `json:"Keywords"`
	}
	if err := c.post(ctx, bingCampaignService, "Keywords/QueryByAdGroupId", accountID, body, &out); err != nil {
		return nil, err
	}
	return out.Keywords, nil
}

// BingMutateResponse is what a campaign management write returns: nothing on
// success, and one entry per failed item.
type BingMutateResponse struct {
	PartialErrors []bingErrorItem `json:"PartialErrors"`
}

// UpdateCampaigns applies campaign updates. Campaigns supports partial update —
// unsent optional fields keep their current values — so callers send the ID
// plus only what they mean to change.
//
// This is emphatically NOT true everywhere in this API: ad extensions and the
// customer management entities are full replacements, where an omitted field is
// a deleted field. Any write tool added for those has to preview the deletions
// it will cause, not just the changes (see the issue's footguns).
func (c *BingClient) UpdateCampaigns(ctx context.Context, accountID string, campaigns []any) (*BingMutateResponse, error) {
	body := map[string]any{"AccountId": accountID, "Campaigns": campaigns}
	var out BingMutateResponse
	if err := c.put(ctx, bingCampaignService, "Campaigns", accountID, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
