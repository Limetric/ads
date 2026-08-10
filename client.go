package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	mathrand "math/rand/v2"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// apiVersion is the Google Ads REST API version this client targets. It is the
// single place the version is spelled; the base and upload URLs derive from it.
const apiVersion = "v23"

// defaultBaseURL is the Google Ads REST API base. Override via
// GOOGLE_ADS_API_BASE_URL (config.BaseURL) — tests point it at httptest.
const defaultBaseURL = "https://googleads.googleapis.com/" + apiVersion

// Client talks to the Google Ads REST API. It is safe for concurrent use.
//
// Note: this is the REST/JSON API (HTTP POST), not gRPC — there is no generated
// protobuf code. Request and response bodies are plain JSON.
type Client struct {
	cfg    *GoogleConfig
	http   *http.Client
	upload *http.Client
	tokens oauth2.TokenSource
}

// NewClient builds a Client from config, resolving the saved sign-in and
// validating credentials first. Resolution comes first because it is what
// decides whether there is a refresh token at all.
func NewClient(ctx context.Context, cfg *GoogleConfig) (*Client, error) {
	if err := cfg.resolveRefreshToken(); err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: 60 * time.Second},
		// Media uploads stream arbitrarily large bodies; http.Client.Timeout
		// covers the entire transfer, so a 60s cap would abort any upload
		// slower than that. Instead the response-header wait is bounded (a
		// server that never answers can't hang the CLI forever) and the
		// transfer itself is bounded by the request context.
		upload: &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 60 * time.Second}},
		tokens: newTokenSource(ctx, cfg.oauth()),
	}, nil
}

// resolveCustomerID returns the customer ID a tool call should act on: the
// explicit argument when given, otherwise the configured default
// (default_customer_id / GOOGLE_ADS_CUSTOMER_ID). Every handler calls this, so
// the default works identically for CLI flags and MCP tool arguments.
//
// A blank explicit value counts as "not given", but a non-blank value that
// normalizes to nothing (e.g. "---") is rejected: it must never silently
// redirect the call — possibly a write — to the default account.
func (c *Client) resolveCustomerID(explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		id := normalizeCustomerID(explicit)
		if id == "" {
			return "", fmt.Errorf("invalid customer ID %q", explicit)
		}
		return id, nil
	}
	if c != nil && c.cfg != nil && c.cfg.DefaultCustomerID != "" {
		return c.cfg.DefaultCustomerID, nil
	}
	return "", fmt.Errorf("customer_id is required — pass --customer-id, or set a default with `ads config google set-customer <id>` (or GOOGLE_ADS_CUSTOMER_ID)")
}

// buildHeaders sets the three headers every Google Ads REST call needs:
// the OAuth bearer token, the developer token, and (optionally) the
// login-customer-id of the manager account.
func (c *Client) buildHeaders(req *http.Request) error {
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
	req.Header.Set("developer-token", dev)

	if c.cfg.LoginCustomerID != "" {
		req.Header.Set("login-customer-id", c.cfg.LoginCustomerID)
	}
	return nil
}

// Retry policy: transient Google Ads responses (429 RESOURCE_EXHAUSTED,
// 5xx) are retried with jittered exponential backoff, honoring Retry-After.
// Reads retry both; writes retry only 429 (the request was rejected before
// processing) because a 5xx may have applied the mutation server-side.
const retryMaxAttempts = 3

// retryBaseDelay is a var so tests can shrink it.
var retryBaseDelay = 500 * time.Millisecond

type retryPolicy int

const (
	retryReads  retryPolicy = iota // retry 429 and 5xx
	retryWrites                    // retry 429 only
)

func (p retryPolicy) retryable(status int) bool {
	if status == http.StatusTooManyRequests {
		return true
	}
	return p == retryReads && status >= 500
}

// backoffDelay computes the sleep before attempt+1: Retry-After when the
// server sent one, otherwise base*2^(attempt-1) plus up to 50% jitter.
func backoffDelay(attempt int, retryAfter string) time.Duration {
	if retryAfter != "" {
		if secs, err := strconv.Atoi(retryAfter); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second
		}
	}
	d := retryBaseDelay << (attempt - 1)
	return d + time.Duration(mathrand.Int64N(int64(d)/2+1))
}

// doJSON issues one JSON request to {baseURL}/{path}, decoding the response
// into out and retrying transient failures per the policy.
func (c *Client) doJSON(ctx context.Context, method, path string, payload []byte, out any, policy retryPolicy) error {
	url := c.cfg.BaseURL + "/" + path
	for attempt := 1; ; attempt++ {
		var body io.Reader
		if payload != nil {
			body = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, body)
		if err != nil {
			return err
		}
		if err := c.buildHeaders(req); err != nil {
			return err
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("call %s: %w", path, err)
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("read %s response: %w", path, err)
		}
		if resp.StatusCode >= 300 {
			apiErr := apiError(resp.StatusCode, data)
			if attempt < retryMaxAttempts && policy.retryable(resp.StatusCode) {
				select {
				case <-time.After(backoffDelay(attempt, resp.Header.Get("Retry-After"))):
					continue
				case <-ctx.Done():
					return apiErr
				}
			}
			return apiErr
		}
		if out != nil && len(data) > 0 {
			if err := json.Unmarshal(data, out); err != nil {
				return fmt.Errorf("decode %s response: %w", path, err)
			}
		}
		return nil
	}
}

func encodeJSON(body any) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	return data, nil
}

// post sends a JSON body to {baseURL}/{path} and decodes the JSON response into
// out. It surfaces Google Ads API error payloads as Go errors. Read-only
// callers get the full retry policy; mutating callers use postWrite.
func (c *Client) post(ctx context.Context, path string, body, out any) error {
	payload, err := encodeJSON(body)
	if err != nil {
		return err
	}
	return c.doJSON(ctx, http.MethodPost, path, payload, out, retryReads)
}

// postWrite is post for mutating endpoints: transient 429s are retried (the
// request was rate-limited before processing), 5xx are not (the mutation may
// have been applied).
func (c *Client) postWrite(ctx context.Context, path string, body, out any) error {
	payload, err := encodeJSON(body)
	if err != nil {
		return err
	}
	return c.doJSON(ctx, http.MethodPost, path, payload, out, retryWrites)
}

// get issues a GET to {baseURL}/{path} and decodes the JSON response into out.
// Mirrors post for read-only endpoints that take no body.
func (c *Client) get(ctx context.Context, path string, out any) error {
	return c.doJSON(ctx, http.MethodGet, path, nil, out, retryReads)
}

// apiStatusError is a non-2xx Google Ads API response. It carries the HTTP
// status so callers can distinguish a definitive client error (4xx — the
// request or credentials are wrong) from a transient server error (5xx). A
// transport failure (no response at all) is a plain error, not this type.
type apiStatusError struct {
	status int
	msg    string
}

func (e *apiStatusError) Error() string { return e.msg }

// isClientError reports whether the status is 4xx: the server understood the
// request and rejected it based on what we sent (bad credentials, permission,
// arguments), i.e. a setup problem the user must fix.
func (e *apiStatusError) isClientError() bool { return e.status >= 400 && e.status < 500 }

// apiErrorDetail mirrors one entry of a Google Ads error's details[] array
// (the GoogleAdsFailure payload) — the part that carries the specific,
// actionable error code and message.
type apiErrorDetail struct {
	Errors []struct {
		ErrorCode map[string]any `json:"errorCode"`
		Message   string         `json:"message"`
	} `json:"errors"`
}

// apiError turns a non-2xx Google Ads response into a readable error.
//
// The top-level message is often generic ("The caller does not have
// permission"). The actionable detail — the specific errorCode and its
// human-readable message (e.g. DEVELOPER_TOKEN_NOT_APPROVED, "apply for Basic
// or Standard access") — lives in error.details[].errors[] of a GoogleAdsFailure.
// We surface those so the CLI tells the user what to actually fix.
func apiError(status int, body []byte) error {
	var e struct {
		Error struct {
			Code    int              `json:"code"`
			Message string           `json:"message"`
			Status  string           `json:"status"`
			Details []apiErrorDetail `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		msg := fmt.Sprintf("google ads API %d (%s): %s", status, e.Error.Status, e.Error.Message)
		if detail := formatAdsFailures(e.Error.Details); detail != "" {
			msg += " — " + detail
		}
		return &apiStatusError{status: status, msg: msg}
	}
	return &apiStatusError{status: status, msg: fmt.Sprintf("google ads API %d: %s", status, string(body))}
}

// formatAdsFailures flattens the GoogleAdsFailure error list into a single
// readable string, e.g.
// "authorizationError.DEVELOPER_TOKEN_NOT_APPROVED: The developer token is …".
// Multiple errors are joined with " | ". Returns "" when there is nothing extra
// to add beyond the top-level message.
func formatAdsFailures(details []apiErrorDetail) string {
	var parts []string
	for _, d := range details {
		for _, e := range d.Errors {
			code := formatErrorCode(e.ErrorCode)
			switch {
			case code != "" && e.Message != "":
				parts = append(parts, code+": "+e.Message)
			case code != "":
				parts = append(parts, code)
			case e.Message != "":
				parts = append(parts, e.Message)
			}
		}
	}
	return strings.Join(parts, " | ")
}

// formatErrorCode renders an errorCode object like
// {"authorizationError":"DEVELOPER_TOKEN_NOT_APPROVED"} as
// "authorizationError.DEVELOPER_TOKEN_NOT_APPROVED". Keys are sorted so the
// output is deterministic when more than one is present (rare).
func formatErrorCode(code map[string]any) string {
	keys := make([]string, 0, len(code))
	for k := range code {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s.%v", k, code[k]))
	}
	return strings.Join(parts, ",")
}

// --- Operations -----------------------------------------------------------
//
// The client uses these Google Ads REST endpoints:
//
//   customers/{id}/googleAds:search          -> Search
//   customers/{id}/googleAds:mutate          -> Mutate
//   customers/{id}:generateKeywordIdeas      -> GenerateKeywordIdeas
//   customers/{id}/recommendations:apply     -> ApplyRecommendations
//   customers/{id}/recommendations:dismiss   -> DismissRecommendations

// searchResponse is one page of a googleAds:search call.
type searchResponse struct {
	Results       []json.RawMessage `json:"results"`
	NextPageToken string            `json:"nextPageToken"`
}

// Search runs a GAQL query for one customer and returns every result row,
// transparently following pagination.
func (c *Client) Search(ctx context.Context, customerID, query string) ([]json.RawMessage, error) {
	customerID = normalizeCustomerID(customerID)
	path := fmt.Sprintf("customers/%s/googleAds:search", customerID)

	var rows []json.RawMessage
	pageToken := ""
	for {
		body := map[string]any{"query": query}
		if pageToken != "" {
			body["pageToken"] = pageToken
		}
		var page searchResponse
		if err := c.post(ctx, path, body, &page); err != nil {
			return nil, err
		}
		rows = append(rows, page.Results...)
		if page.NextPageToken == "" {
			break
		}
		pageToken = page.NextPageToken
	}
	return rows, nil
}

// ListAccessibleCustomers returns the bare customer IDs the authenticated user
// can access. It calls customers:listAccessibleCustomers, which needs only a
// valid OAuth token and developer token — no customer or login-customer-id — so
// it is the right call to verify a fresh setup works end to end.
func (c *Client) ListAccessibleCustomers(ctx context.Context) ([]string, error) {
	var out struct {
		ResourceNames []string `json:"resourceNames"`
	}
	if err := c.get(ctx, "customers:listAccessibleCustomers", &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.ResourceNames))
	for _, rn := range out.ResourceNames {
		ids = append(ids, strings.TrimPrefix(rn, "customers/"))
	}
	return ids, nil
}

// MutateResponse is the result of a googleAds:mutate call.
type MutateResponse struct {
	// MutateOperationResponses is the response field used by googleAds:mutate.
	MutateOperationResponses []json.RawMessage `json:"mutateOperationResponses"`
	// Results is retained for compatibility with older mock servers and any
	// endpoint variant that returns the conventional results field.
	Results       []json.RawMessage `json:"results"`
	PartialErrors json.RawMessage   `json:"partialFailureError,omitempty"`
}

func (r *MutateResponse) operationResults() []json.RawMessage {
	if len(r.MutateOperationResponses) > 0 {
		return r.MutateOperationResponses
	}
	return r.Results
}

// Mutate applies a batch of mutate operations for one customer. Callers are
// responsible for guarding writes (see safety.go) before reaching this point.
//
// Unknown top-level operation keys are rejected client-side (validateMutateOps)
// before any HTTP traffic, and partialFailure is enabled so a bad op in a batch
// surfaces as a per-op error rather than failing the whole request.
func (c *Client) Mutate(ctx context.Context, customerID string, ops []any) (*MutateResponse, error) {
	if err := validateMutateOps(ops); err != nil {
		return nil, err
	}
	customerID = normalizeCustomerID(customerID)
	path := fmt.Sprintf("customers/%s/googleAds:mutate", customerID)
	body := map[string]any{"mutateOperations": ops, "partialFailure": true}
	var out MutateResponse
	if err := c.postWrite(ctx, path, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// YouTubeVideoUploadResponse is returned after a resumable video upload is
// finalized. The video is processed asynchronously; query you_tube_video_upload
// before creating a YouTube video asset from its video_id.
type YouTubeVideoUploadResponse struct {
	ResourceName string `json:"resourceName"`
}

// UploadYouTubeVideo uploads a local MP4 to the Google-managed YouTube channel
// associated with the Ads account. Google-managed uploads are always unlisted.
func (c *Client) UploadYouTubeVideo(ctx context.Context, customerID, filePath, title, description string) (*YouTubeVideoUploadResponse, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open video file: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat video file: %w", err)
	}
	if info.Size() == 0 {
		return nil, fmt.Errorf("video file is empty")
	}

	customerID = normalizeCustomerID(customerID)
	metadata := map[string]any{
		"customerId": customerID,
		"youTubeVideoUpload": map[string]any{
			"videoTitle":       title,
			"videoDescription": description,
			"videoPrivacy":     "UNLISTED",
		},
	}
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(metadata); err != nil {
		return nil, fmt.Errorf("encode video metadata: %w", err)
	}
	uploadBaseURL := strings.TrimSuffix(c.cfg.BaseURL, "/"+apiVersion) + "/resumable/upload/" + apiVersion
	startURL := fmt.Sprintf("%s/customers/%s/youTubeVideoUploads:create", uploadBaseURL, customerID)
	startReq, err := http.NewRequestWithContext(ctx, http.MethodPost, startURL, &body)
	if err != nil {
		return nil, err
	}
	if err := c.buildHeaders(startReq); err != nil {
		return nil, err
	}
	startReq.Header.Set("X-Goog-Upload-Protocol", "resumable")
	startReq.Header.Set("X-Goog-Upload-Command", "start")
	startReq.Header.Set("X-Goog-Upload-Header-Content-Length", strconv.FormatInt(info.Size(), 10))
	startResp, err := c.http.Do(startReq)
	if err != nil {
		return nil, fmt.Errorf("start YouTube video upload: %w", err)
	}
	defer startResp.Body.Close()
	startData, err := io.ReadAll(startResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read video upload start response: %w", err)
	}
	if startResp.StatusCode >= 300 {
		return nil, apiError(startResp.StatusCode, startData)
	}
	uploadURL := startResp.Header.Get("X-Goog-Upload-URL")
	if uploadURL == "" {
		return nil, fmt.Errorf("start YouTube video upload: response omitted X-Goog-Upload-URL")
	}

	uploadReq, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, file)
	if err != nil {
		return nil, err
	}
	if err := c.buildHeaders(uploadReq); err != nil {
		return nil, err
	}
	uploadReq.Header.Set("Content-Type", "video/mp4")
	uploadReq.Header.Set("X-Goog-Upload-Offset", "0")
	uploadReq.Header.Set("X-Goog-Upload-Command", "upload, finalize")
	uploadReq.ContentLength = info.Size()
	uploadResp, err := c.upload.Do(uploadReq)
	if err != nil {
		return nil, fmt.Errorf("upload YouTube video bytes: %w", err)
	}
	defer uploadResp.Body.Close()
	uploadData, err := io.ReadAll(uploadResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read video upload response: %w", err)
	}
	if uploadResp.StatusCode >= 300 {
		return nil, apiError(uploadResp.StatusCode, uploadData)
	}
	var response YouTubeVideoUploadResponse
	if err := json.Unmarshal(uploadData, &response); err != nil {
		return nil, fmt.Errorf("decode video upload response: %w", err)
	}
	if response.ResourceName == "" {
		return nil, fmt.Errorf("video upload response omitted resourceName")
	}
	return &response, nil
}

// GenerateKeywordIdeas calls the Keyword Planner generateKeywordIdeas endpoint
// for a set of seed keywords, returning the raw idea result rows.
func (c *Client) GenerateKeywordIdeas(ctx context.Context, customerID string, seedKeywords []string, pageSize int) ([]json.RawMessage, error) {
	customerID = normalizeCustomerID(customerID)
	path := fmt.Sprintf("customers/%s:generateKeywordIdeas", customerID)
	if pageSize <= 0 {
		pageSize = 50
	}
	body := map[string]any{
		"keywordSeed":        map[string]any{"keywords": seedKeywords},
		"language":           "languageConstants/1000",
		"pageSize":           pageSize,
		"keywordPlanNetwork": "GOOGLE_SEARCH",
	}
	var out struct {
		Results []json.RawMessage `json:"results"`
	}
	if err := c.post(ctx, path, body, &out); err != nil {
		return nil, err
	}
	return out.Results, nil
}

// RecommendationResponse is the result of a recommendations:apply or
// recommendations:dismiss call.
type RecommendationResponse struct {
	Results       []json.RawMessage `json:"results"`
	PartialErrors json.RawMessage   `json:"partialFailureError,omitempty"`
}

// recommendationOps turns full recommendation resource names into the
// {"resourceName": …} operation objects both RPCs expect.
func recommendationOps(resourceNames []string) []map[string]any {
	ops := make([]map[string]any, len(resourceNames))
	for i, rn := range resourceNames {
		ops[i] = map[string]any{"resourceName": rn}
	}
	return ops
}

// ApplyRecommendations applies recommendations via the dedicated
// recommendations:apply RPC. resourceNames must be full paths
// (customers/{cid}/recommendations/{id}).
func (c *Client) ApplyRecommendations(ctx context.Context, customerID string, resourceNames []string) (*RecommendationResponse, error) {
	customerID = normalizeCustomerID(customerID)
	path := fmt.Sprintf("customers/%s/recommendations:apply", customerID)
	body := map[string]any{"operations": recommendationOps(resourceNames), "partialFailure": true}
	var out RecommendationResponse
	if err := c.postWrite(ctx, path, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DismissRecommendations dismisses recommendations via the dedicated
// recommendations:dismiss RPC.
func (c *Client) DismissRecommendations(ctx context.Context, customerID string, resourceNames []string) (*RecommendationResponse, error) {
	customerID = normalizeCustomerID(customerID)
	path := fmt.Sprintf("customers/%s/recommendations:dismiss", customerID)
	body := map[string]any{"operations": recommendationOps(resourceNames)}
	var out RecommendationResponse
	if err := c.postWrite(ctx, path, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
