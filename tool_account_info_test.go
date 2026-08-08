package main

import (
	"strings"
	"testing"
)

func TestRunAccountInfo(t *testing.T) {
	results := `[{"customer":{"descriptiveName":"Limetric","currencyCode":"EUR","timeZone":"Europe/Amsterdam","manager":false,"testAccount":true,"autoTaggingEnabled":true}}]`
	srv := gaqlServer(t, results, "FROM customer", "customer.currency_code")
	defer srv.Close()

	res, err := runAccountInfo(t.Context(), newTestClient(t, srv), AccountInfoArgs{CustomerID: "123-456-7890"})
	if err != nil {
		t.Fatalf("runAccountInfo: %v", err)
	}
	if res.CustomerID != "1234567890" {
		t.Errorf("CustomerID = %q, want normalized 1234567890", res.CustomerID)
	}
	if res.DescriptiveName != "Limetric" || res.CurrencyCode != "EUR" || res.TimeZone != "Europe/Amsterdam" {
		t.Errorf("unexpected account info: %+v", res)
	}
	if res.IsManager || !res.IsTestAccount || !res.AutoTaggingEnabled {
		t.Errorf("unexpected flags: %+v", res)
	}
}

func TestRunAccountInfo_NoRows(t *testing.T) {
	srv := gaqlServer(t, `[]`)
	defer srv.Close()
	if _, err := runAccountInfo(t.Context(), newTestClient(t, srv), AccountInfoArgs{CustomerID: "1"}); err == nil {
		t.Fatal("expected error when the customer query returns no rows")
	}
}

func TestRunAccountInfo_RequiresCustomerID(t *testing.T) {
	if _, err := runAccountInfo(t.Context(), nil, AccountInfoArgs{}); err == nil {
		t.Fatal("expected error for missing customer_id")
	}
}

func TestCLI_AccountsInfoCommand(t *testing.T) {
	useTempState(t)
	clearAdsEnv(t)
	srv := gaqlServer(t, `[{"customer":{"descriptiveName":"Shop","currencyCode":"USD","timeZone":"America/New_York"}}]`)
	defer srv.Close()
	t.Setenv("GOOGLE_ADS_API_BASE_URL", srv.URL)

	out, err := runCLI(t, "google", "accounts", "info", "--customer-id", "1234567890")
	if err != nil {
		t.Fatalf("accounts info: %v\noutput: %s", err, out)
	}
	for _, want := range []string{`"currency_code": "USD"`, `"descriptive_name": "Shop"`} {
		if !strings.Contains(out, want) {
			t.Errorf("accounts info output missing %q:\n%s", want, out)
		}
	}
}
