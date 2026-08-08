package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runCLI executes the root command with args, capturing combined output. It
// isolates config to a temp HOME and resets the shared command's args after.
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	configPath = ""
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs(args)
	t.Cleanup(func() {
		configPath = ""
		rootCmd.SetArgs(nil)
	})
	err := rootCmd.Execute()
	return out.String(), err
}

func TestCLI_ConfigPath(t *testing.T) {
	t.Run("explicit path", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "custom config.toml")
		out, err := runCLI(t, "config", "path", "--config", path)
		if err != nil {
			t.Fatalf("execute config path: %v\noutput: %s", err, out)
		}
		if want := path + "\n"; out != want {
			t.Errorf("config path output = %q, want %q", out, want)
		}
	})

	t.Run("default path", func(t *testing.T) {
		useTempState(t)
		dir, err := userConfigDir()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, defaultConfigFile)
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}

		out, err := runCLI(t, "config", "path")
		if err != nil {
			t.Fatalf("execute config path: %v\noutput: %s", err, out)
		}
		if want := path + "\n"; out != want {
			t.Errorf("config path output = %q, want %q", out, want)
		}
	})

	t.Run("environment only", func(t *testing.T) {
		useTempState(t)
		t.Setenv("GOOGLE_ADS_DEVELOPER_TOKEN", "must-not-be-printed")
		out, err := runCLI(t, "config", "path")
		if err != nil {
			t.Fatalf("execute config path: %v\noutput: %s", err, out)
		}
		if want := "environment only (no config file)\n"; out != want {
			t.Errorf("config path output = %q, want %q", out, want)
		}
		if strings.Contains(out, "must-not-be-printed") {
			t.Errorf("config path output leaked a credential: %q", out)
		}
	})

	t.Run("rejects arguments", func(t *testing.T) {
		if out, err := runCLI(t, "config", "path", "extra"); err == nil {
			t.Fatalf("config path should reject arguments; output: %s", out)
		}
	})
}

func TestCLI_ConfigSetCustomer(t *testing.T) {
	t.Run("writes and preserves existing keys", func(t *testing.T) {
		useTempState(t)
		clearAdsEnv(t)
		dir, err := userConfigDir()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, defaultConfigFile)
		if err := os.WriteFile(path, []byte("developer_token = \"file-tok\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		out, err := runCLI(t, "config", "google", "set-customer", "123-456-7890")
		if err != nil {
			t.Fatalf("set-customer: %v\noutput: %s", err, out)
		}
		if !strings.Contains(out, "1234567890") || !strings.Contains(out, path) {
			t.Errorf("unexpected set-customer output: %q", out)
		}

		cfg, err := loadGoogleConfig("")
		if err != nil {
			t.Fatalf("loadGoogleConfig: %v", err)
		}
		if cfg.DefaultCustomerID != "1234567890" {
			t.Errorf("DefaultCustomerID = %q, want 1234567890", cfg.DefaultCustomerID)
		}
		if cfg.DeveloperToken != "file-tok" {
			t.Errorf("existing key should be preserved, got %q", cfg.DeveloperToken)
		}
	})

	t.Run("creates the config file when missing", func(t *testing.T) {
		useTempState(t)
		clearAdsEnv(t)
		if out, err := runCLI(t, "config", "google", "set-customer", "1234567890"); err != nil {
			t.Fatalf("set-customer: %v\noutput: %s", err, out)
		}
		cfg, err := loadGoogleConfig("")
		if err != nil {
			t.Fatalf("loadGoogleConfig: %v", err)
		}
		if cfg.DefaultCustomerID != "1234567890" {
			t.Errorf("DefaultCustomerID = %q, want 1234567890", cfg.DefaultCustomerID)
		}
	})

	t.Run("honors an explicit --config path", func(t *testing.T) {
		useTempState(t)
		clearAdsEnv(t)
		// Nested path: missing parent directories must be created.
		path := filepath.Join(t.TempDir(), "nested", "dir", "custom.toml")
		if out, err := runCLI(t, "config", "google", "set-customer", "1234567890", "--config", path); err != nil {
			t.Fatalf("set-customer --config: %v\noutput: %s", err, out)
		}
		cfg, err := loadGoogleConfig(path)
		if err != nil {
			t.Fatalf("loadGoogleConfig: %v", err)
		}
		if cfg.DefaultCustomerID != "1234567890" {
			t.Errorf("DefaultCustomerID = %q, want 1234567890", cfg.DefaultCustomerID)
		}
	})

	t.Run("rejects malformed IDs", func(t *testing.T) {
		useTempState(t)
		for _, bad := range []string{"12345", "abcdefghij", "12345678901"} {
			if out, err := runCLI(t, "config", "google", "set-customer", bad); err == nil {
				t.Errorf("set-customer %q should fail; output: %s", bad, out)
			}
		}
	})
}

func TestCLI_ConfigShow(t *testing.T) {
	useTempState(t)
	clearAdsEnv(t)
	t.Setenv("GOOGLE_ADS_REFRESH_TOKEN", "super-secret-refresh-9876")
	t.Setenv("GOOGLE_ADS_CUSTOMER_ID", "123-456-7890")

	out, err := runCLI(t, "config", "show")
	if err != nil {
		t.Fatalf("config show: %v\noutput: %s", err, out)
	}
	if strings.Contains(out, "super-secret-refresh-9876") {
		t.Errorf("config show leaked a credential:\n%s", out)
	}
	for _, want := range []string{
		"refresh token:        set (…9876)",
		"developer token:      (not set)",
		"default customer id:  1234567890",
		"config file:          (none — environment only)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("config show missing %q:\n%s", want, out)
		}
	}
}

func TestCLI_AccountsCommand(t *testing.T) {
	useTempState(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"customerClient":{"id":"111"}}]}`))
	}))
	defer srv.Close()
	t.Setenv("GOOGLE_ADS_API_BASE_URL", srv.URL) // non-prod → skips OAuth/creds
	t.Setenv("GOOGLE_ADS_LOGIN_CUSTOMER_ID", "1234567890")

	out, err := runCLI(t, "google", "accounts")
	if err != nil {
		t.Fatalf("execute accounts: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "customer_ids") || !strings.Contains(out, "111") {
		t.Errorf("unexpected accounts output:\n%s", out)
	}
}

func TestCLI_DefaultCustomerID(t *testing.T) {
	useTempState(t)
	clearAdsEnv(t)

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()
	t.Setenv("GOOGLE_ADS_API_BASE_URL", srv.URL)

	t.Run("missing customer id is actionable", func(t *testing.T) {
		out, err := runCLI(t, "google", "campaigns")
		if err == nil {
			t.Fatalf("campaigns without a customer ID should fail; output: %s", out)
		}
		if !strings.Contains(err.Error(), "config google set-customer") {
			t.Errorf("error should point at `config google set-customer`: %v", err)
		}
	})

	t.Run("GOOGLE_ADS_CUSTOMER_ID fills in", func(t *testing.T) {
		t.Setenv("GOOGLE_ADS_CUSTOMER_ID", "123-456-7890")
		out, err := runCLI(t, "google", "campaigns")
		if err != nil {
			t.Fatalf("execute campaigns: %v\noutput: %s", err, out)
		}
		if !strings.Contains(gotPath, "customers/1234567890") {
			t.Errorf("query should hit the default customer, got path %q", gotPath)
		}
	})
}

func TestCLI_ReadCommandFormats(t *testing.T) {
	useTempState(t)
	clearAdsEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// camelCase keys, exactly as the REST API returns them.
		_, _ = w.Write([]byte(`{"results":[{"campaign":{"id":"123","name":"Brand"},"metrics":{"clicks":"7","costMicros":"5000000"}}]}`))
	}))
	defer srv.Close()
	t.Setenv("GOOGLE_ADS_API_BASE_URL", srv.URL)

	// Subtest order matters: flag variables persist across Execute calls on the
	// shared rootCmd, so the default-format case must run before any --format.
	t.Run("json is the default", func(t *testing.T) {
		out, err := runCLI(t, "google", "campaigns", "--customer-id", "1234567890")
		if err != nil {
			t.Fatalf("campaigns: %v\noutput: %s", err, out)
		}
		if !strings.Contains(out, `"total_count": 1`) {
			t.Errorf("json output missing total_count:\n%s", out)
		}
	})

	t.Run("table", func(t *testing.T) {
		out, err := runCLI(t, "google", "campaigns", "--customer-id", "1234567890", "--format", "table")
		if err != nil {
			t.Fatalf("campaigns --format table: %v\noutput: %s", err, out)
		}
		// 5000000 proves camelCase REST keys resolve for snake_case columns.
		for _, want := range []string{"campaign.id", "campaign.name", "123", "Brand", "5000000"} {
			if !strings.Contains(out, want) {
				t.Errorf("table output missing %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, "{") {
			t.Errorf("table output should not contain JSON:\n%s", out)
		}
	})

	t.Run("csv", func(t *testing.T) {
		out, err := runCLI(t, "google", "campaigns", "--customer-id", "1234567890", "--format", "csv")
		if err != nil {
			t.Fatalf("campaigns --format csv: %v\noutput: %s", err, out)
		}
		if !strings.Contains(out, "123,Brand") {
			t.Errorf("csv output missing row:\n%s", out)
		}
	})

	t.Run("unknown format errors", func(t *testing.T) {
		if out, err := runCLI(t, "google", "campaigns", "--customer-id", "1234567890", "--format", "yaml"); err == nil {
			t.Fatalf("unknown format should fail; output: %s", out)
		}
	})

	t.Run("search table uses the query's fields", func(t *testing.T) {
		out, err := runCLI(t, "google", "search", "--customer-id", "1234567890",
			"--query", "SELECT campaign.id, campaign.name FROM campaign", "--format", "table")
		if err != nil {
			t.Fatalf("search --format table: %v\noutput: %s", err, out)
		}
		if !strings.Contains(out, "campaign.id") || !strings.Contains(out, "Brand") {
			t.Errorf("search table output unexpected:\n%s", out)
		}
	})
}

func TestCLI_DoctorReportsMissingCredentials(t *testing.T) {
	useTempState(t)
	clearAdsEnv(t) // production base URL, no creds → NOT READY

	out, err := runCLI(t, "doctor")
	if err == nil {
		t.Errorf("doctor should fail when credentials are missing")
	}
	for _, want := range []string{"developer token:    MISSING", "login customer id:  (none)", "default customer:   (none)", "NOT READY"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output missing %q:\n%s", want, out)
		}
	}
}

func TestCLI_RejectsUnknownCommand(t *testing.T) {
	if _, err := runCLI(t, "definitely-not-a-command"); err == nil {
		t.Error("expected an error for an unknown command")
	}
}
