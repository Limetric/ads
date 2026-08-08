package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestPrintJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := printJSON(&buf, map[string]any{"a": 1, "b": "two"}); err != nil {
		t.Fatalf("printJSON: %v", err)
	}
	if !strings.HasSuffix(buf.String(), "\n") {
		t.Error("printJSON output should end with a newline")
	}
	var back map[string]any
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("printJSON produced invalid JSON: %v", err)
	}
	if back["b"] != "two" {
		t.Errorf("round-trip mismatch: %v", back)
	}
	if !strings.Contains(buf.String(), "  ") {
		t.Error("printJSON output should be indented")
	}
}

func TestVersionVerboseString(t *testing.T) {
	s := versionVerboseString()
	for _, want := range []string{"ads", "go:", "platform:"} {
		if !strings.Contains(s, want) {
			t.Errorf("version string missing %q:\n%s", want, s)
		}
	}
}

func TestDoctorHelpers(t *testing.T) {
	if present("") != "MISSING" || present("x") != "set" {
		t.Error("present() wrong")
	}
	if orNone("") != "(none)" || orNone("abc") != "abc" {
		t.Error("orNone() wrong")
	}
}

func TestAPIError(t *testing.T) {
	// Structured Google Ads error payload.
	jsonErr := apiError(400, []byte(`{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"bad field"}}`))
	if !strings.Contains(jsonErr.Error(), "bad field") || !strings.Contains(jsonErr.Error(), "INVALID_ARGUMENT") {
		t.Errorf("structured error not surfaced: %v", jsonErr)
	}
	// Non-JSON body falls back to the raw text.
	plain := apiError(500, []byte("API exploded"))
	if !strings.Contains(plain.Error(), "500") || !strings.Contains(plain.Error(), "API exploded") {
		t.Errorf("plain-text fallback not surfaced: %v", plain)
	}
}

// TestAPIError_GoogleAdsFailureDetails checks that the inner GoogleAdsFailure —
// where the API hides the actionable error code and message — is surfaced, not
// just the generic top-level "The caller does not have permission".
func TestAPIError_GoogleAdsFailureDetails(t *testing.T) {
	body := []byte(`{
      "error": {
        "code": 403,
        "message": "The caller does not have permission",
        "status": "PERMISSION_DENIED",
        "details": [
          {
            "@type": "type.googleapis.com/google.ads.googleads.v23.errors.GoogleAdsFailure",
            "errors": [
              {
                "errorCode": {"authorizationError": "DEVELOPER_TOKEN_NOT_APPROVED"},
                "message": "The developer token is only approved for use with test accounts. To access non-test accounts, apply for Basic or Standard access."
              }
            ]
          }
        ]
      }
    }`)
	err := apiError(403, body)
	msg := err.Error()
	for _, want := range []string{
		"PERMISSION_DENIED",
		"DEVELOPER_TOKEN_NOT_APPROVED",
		"apply for Basic or Standard access",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error missing %q:\n%s", want, msg)
		}
	}
	// It carries the HTTP status so callers can classify it (4xx = definitive).
	var apiErr *apiStatusError
	if !errors.As(err, &apiErr) || apiErr.status != 403 {
		t.Errorf("apiError should carry status 403, got %v", err)
	}
}

func TestParseSitelinkFlag(t *testing.T) {
	sl, err := parseSitelinkFlag("Shop|https://x.com/shop|Great deals|Today only")
	if err != nil {
		t.Fatalf("parseSitelinkFlag: %v", err)
	}
	if sl.LinkText != "Shop" || sl.FinalURL != "https://x.com/shop" || sl.Description1 != "Great deals" || sl.Description2 != "Today only" {
		t.Errorf("parsed wrong: %+v", sl)
	}
	if _, err := parseSitelinkFlag("only|three|fields"); err == nil {
		t.Error("expected error for wrong field count")
	}
}

func TestEnrichCPA(t *testing.T) {
	t.Run("computes cpa when conversions are positive", func(t *testing.T) {
		rows := enrichCPA([]json.RawMessage{raw(`{"metrics":{"costMicros":"5000000","conversions":2}}`)})
		if !strings.Contains(string(rows[0]), `"cpa":"2.50"`) {
			t.Errorf("expected cpa 2.50, got %s", rows[0])
		}
	})
	t.Run("omits cpa when conversions are zero", func(t *testing.T) {
		rows := enrichCPA([]json.RawMessage{raw(`{"metrics":{"costMicros":"5000000","conversions":0}}`)})
		if strings.Contains(string(rows[0]), `"cpa"`) {
			t.Errorf("cpa should be omitted when conversions == 0: %s", rows[0])
		}
	})
	t.Run("leaves rows without metrics untouched", func(t *testing.T) {
		in := raw(`{"campaign":{"id":"1"}}`)
		rows := enrichCPA([]json.RawMessage{in})
		if strings.Contains(string(rows[0]), `"cpa"`) {
			t.Errorf("row without metrics should be unchanged: %s", rows[0])
		}
	})
}
