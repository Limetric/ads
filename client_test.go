package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUploadYouTubeVideo(t *testing.T) {
	videoData := []byte("fake mp4 bytes")
	videoPath := filepath.Join(t.TempDir(), "creative.mp4")
	if err := os.WriteFile(videoPath, videoData, 0o600); err != nil {
		t.Fatal(err)
	}

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/resumable/upload/v23/customers/1234567890/youTubeVideoUploads:create":
			if r.Method != http.MethodPost {
				t.Errorf("start method = %s, want POST", r.Method)
			}
			if got := r.Header.Get("X-Goog-Upload-Protocol"); got != "resumable" {
				t.Errorf("upload protocol = %q", got)
			}
			if got := r.Header.Get("X-Goog-Upload-Header-Content-Length"); got != "14" {
				t.Errorf("upload length = %q", got)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			upload, _ := body["youTubeVideoUpload"].(map[string]any)
			if upload["videoPrivacy"] != "UNLISTED" || upload["videoTitle"] != "CMV creative" {
				t.Errorf("unexpected metadata: %v", upload)
			}
			w.Header().Set("X-Goog-Upload-URL", srv.URL+"/upload")
			w.WriteHeader(http.StatusOK)
		case "/upload":
			if r.Method != http.MethodPut {
				t.Errorf("upload method = %s, want PUT", r.Method)
			}
			if got := r.Header.Get("X-Goog-Upload-Command"); got != "upload, finalize" {
				t.Errorf("upload command = %q", got)
			}
			got, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(videoData) {
				t.Errorf("uploaded bytes = %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"resourceName":"customers/1234567890/youTubeVideoUploads/456"}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	response, err := newTestClient(t, srv).UploadYouTubeVideo(t.Context(), "123-456-7890", videoPath, "CMV creative", "Description")
	if err != nil {
		t.Fatalf("UploadYouTubeVideo: %v", err)
	}
	if response.ResourceName != "customers/1234567890/youTubeVideoUploads/456" {
		t.Fatalf("resource name = %q", response.ResourceName)
	}
}

func TestValidateMutateOps(t *testing.T) {
	t.Run("accepts known keys", func(t *testing.T) {
		ops := []any{
			map[string]any{"adGroupOperation": map[string]any{"create": map[string]any{}}},
			map[string]any{"campaignBudgetOperation": map[string]any{"create": map[string]any{}}},
		}
		if err := validateMutateOps(ops); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
	t.Run("rejects recommendation ops", func(t *testing.T) {
		ops := []any{map[string]any{"dismissRecommendationOperation": map[string]any{"resourceName": "x"}}}
		err := validateMutateOps(ops)
		if err == nil || !strings.Contains(err.Error(), "dismissRecommendationOperation") {
			t.Errorf("expected rejection, got %v", err)
		}
		if !strings.Contains(err.Error(), "apply_recommendations / dismiss_recommendations") {
			t.Errorf("error missing guidance: %v", err)
		}
	})
	t.Run("rejects apply recommendation op", func(t *testing.T) {
		ops := []any{map[string]any{"applyRecommendationOperation": map[string]any{"resourceName": "x"}}}
		if err := validateMutateOps(ops); err == nil {
			t.Error("expected rejection of applyRecommendationOperation")
		}
	})
	t.Run("rejects non-object op", func(t *testing.T) {
		if err := validateMutateOps([]any{"not-an-object"}); err == nil {
			t.Error("expected rejection of non-object op")
		}
	})
}

func TestMutate_ValidatesAndSetsPartialFailure(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"resourceName":"customers/1/campaignBudgets/2"}]}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	// Bad op never reaches the server.
	if _, err := c.Mutate(context.Background(), "123", []any{map[string]any{"bogusOperation": map[string]any{}}}); err == nil {
		t.Fatal("expected validation error for unknown op key")
	}
	if gotBody != nil {
		t.Fatal("server should not have been called for an invalid op")
	}

	// Good op is sent with partialFailure set.
	_, err := c.Mutate(context.Background(), "123", []any{map[string]any{"campaignBudgetOperation": map[string]any{"update": map[string]any{}}}})
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if pf, ok := gotBody["partialFailure"].(bool); !ok || !pf {
		t.Errorf("partialFailure not set in body: %v", gotBody)
	}
}

func TestGenerateKeywordIdeas(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/customers/1234567890:generateKeywordIdeas") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"text":"shoes"},{"text":"boots"}]}`))
	}))
	defer srv.Close()

	rows, err := newTestClient(t, srv).GenerateKeywordIdeas(context.Background(), "123-456-7890", []string{"shoes"}, 0)
	if err != nil {
		t.Fatalf("GenerateKeywordIdeas: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("got %d ideas, want 2", len(rows))
	}
	// Defaults applied: pageSize 50, search network, seed keyword present.
	if pageSize, _ := asFloat(gotBody["pageSize"]); pageSize != 50 {
		t.Errorf("pageSize default = %v, want 50", gotBody["pageSize"])
	}
	if gotBody["keywordPlanNetwork"] != "GOOGLE_SEARCH" {
		t.Errorf("keywordPlanNetwork = %v", gotBody["keywordPlanNetwork"])
	}
}

func TestListAccessibleCustomers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/customers:listAccessibleCustomers" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("developer-token") == "" {
			t.Error("developer-token header not set")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"resourceNames":["customers/1234567890","customers/9876543210"]}`))
	}))
	defer srv.Close()

	cfg := &GoogleConfig{BaseURL: srv.URL} // non-prod base → isTest(), lenient auth
	c, err := NewClient(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := c.ListAccessibleCustomers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"1234567890", "9876543210"}
	if len(ids) != len(want) || ids[0] != want[0] || ids[1] != want[1] {
		t.Fatalf("got %v, want %v", ids, want)
	}
}

func TestApplyAndDismissRecommendations(t *testing.T) {
	t.Run("apply sends partialFailure and resource names", func(t *testing.T) {
		var gotBody map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, "/recommendations:apply") {
				t.Errorf("path = %q", r.URL.Path)
			}
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &gotBody)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"results":[{"resourceName":"customers/1/recommendations/r1"}]}`))
		}))
		defer srv.Close()

		res, err := newTestClient(t, srv).ApplyRecommendations(context.Background(), "1", []string{"customers/1/recommendations/r1"})
		if err != nil {
			t.Fatalf("ApplyRecommendations: %v", err)
		}
		if len(res.Results) != 1 {
			t.Errorf("got %d results, want 1", len(res.Results))
		}
		if pf, _ := gotBody["partialFailure"].(bool); !pf {
			t.Error("apply should set partialFailure")
		}
		ops, _ := gotBody["operations"].([]any)
		if len(ops) != 1 {
			t.Fatalf("operations = %v", gotBody["operations"])
		}
	})

	t.Run("dismiss omits partialFailure", func(t *testing.T) {
		var gotBody map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, "/recommendations:dismiss") {
				t.Errorf("path = %q", r.URL.Path)
			}
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &gotBody)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"results":[{}]}`))
		}))
		defer srv.Close()

		if _, err := newTestClient(t, srv).DismissRecommendations(context.Background(), "1", []string{"customers/1/recommendations/r1"}); err != nil {
			t.Fatalf("DismissRecommendations: %v", err)
		}
		if _, present := gotBody["partialFailure"]; present {
			t.Error("dismiss should not send partialFailure")
		}
	})
}

func TestRetry_ReadsRetryTransientErrors(t *testing.T) {
	old := retryBaseDelay
	retryBaseDelay = time.Millisecond
	defer func() { retryBaseDelay = old }()

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		switch calls {
		case 1:
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited","status":"RESOURCE_EXHAUSTED"}}`))
		case 2:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"backend","status":"INTERNAL"}}`))
		default:
			_, _ = w.Write([]byte(`{"results":[{"campaign":{"id":"1"}}]}`))
		}
	}))
	defer srv.Close()

	rows, err := newTestClient(t, srv).Search(context.Background(), "123", "SELECT campaign.id FROM campaign")
	if err != nil {
		t.Fatalf("Search should succeed after retries: %v", err)
	}
	if calls != 3 || len(rows) != 1 {
		t.Errorf("calls = %d (want 3), rows = %d (want 1)", calls, len(rows))
	}
}

func TestRetry_ReadsGiveUpAfterMaxAttempts(t *testing.T) {
	old := retryBaseDelay
	retryBaseDelay = time.Millisecond
	defer func() { retryBaseDelay = old }()

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"unavailable","status":"UNAVAILABLE"}}`))
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv).Search(context.Background(), "123", "SELECT campaign.id FROM campaign")
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if calls != retryMaxAttempts {
		t.Errorf("calls = %d, want %d", calls, retryMaxAttempts)
	}
}

func TestRetry_MutateDoesNotRetry5xx(t *testing.T) {
	old := retryBaseDelay
	retryBaseDelay = time.Millisecond
	defer func() { retryBaseDelay = old }()

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"backend","status":"INTERNAL"}}`))
	}))
	defer srv.Close()

	op := map[string]any{"campaignBudgetOperation": map[string]any{"update": map[string]any{}}}
	if _, err := newTestClient(t, srv).Mutate(context.Background(), "123", []any{op}); err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("a 5xx mutate must not be retried (may have applied server-side): calls = %d", calls)
	}
}

func TestRetry_MutateRetries429(t *testing.T) {
	old := retryBaseDelay
	retryBaseDelay = time.Millisecond
	defer func() { retryBaseDelay = old }()

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited","status":"RESOURCE_EXHAUSTED"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"results":[{}]}`))
	}))
	defer srv.Close()

	op := map[string]any{"campaignBudgetOperation": map[string]any{"update": map[string]any{}}}
	if _, err := newTestClient(t, srv).Mutate(context.Background(), "123", []any{op}); err != nil {
		t.Fatalf("Mutate should succeed after a 429 retry: %v", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}
