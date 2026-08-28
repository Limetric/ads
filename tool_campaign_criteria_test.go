package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// criteriaServer fakes a googleAds:search that returns the given rows and
// records the query it was asked.
func criteriaServer(t *testing.T, query *string, rows string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body struct {
			Query string `json:"query"`
		}
		_ = decodeJSONBody(r, &body)
		if query != nil {
			*query = body.Query
		}
		_, _ = w.Write([]byte(`{"results":` + rows + `}`))
	}))
}

const criteriaRows = `[
  {"campaign":{"id":"111"},"campaignCriterion":{"criterionId":"222","type":"LOCATION","negative":false,"status":"ENABLED","location":{"geoTargetConstant":"geoTargetConstants/2840"}}},
  {"campaign":{"id":"111"},"campaignCriterion":{"criterionId":"333","type":"AD_SCHEDULE","negative":false,"status":"ENABLED","adSchedule":{"dayOfWeek":"MONDAY","startHour":9,"endHour":17}}}
]`

func TestCampaignCriteria_ListsWithRemovalIDs(t *testing.T) {
	useTempState(t)
	var query string
	srv := criteriaServer(t, &query, criteriaRows)
	defer srv.Close()
	c := newTestClient(t, srv)

	res, err := runCampaignCriteria(t.Context(), c, CampaignCriteriaArgs{CustomerID: "1", CampaignID: "111"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if res.TotalCount != 2 {
		t.Fatalf("total_count = %d, want 2", res.TotalCount)
	}
	if !strings.Contains(query, "FROM campaign_criterion") || !strings.Contains(query, "campaign.id = 111") {
		t.Errorf("unexpected query: %s", query)
	}
	// Removed criteria are already gone; listing them would only offer IDs
	// that cannot be removed again.
	if !strings.Contains(query, "campaign_criterion.status != 'REMOVED'") {
		t.Errorf("query should exclude removed criteria: %s", query)
	}
	// The whole point of the read side: the ID remove_entity takes, ready to
	// pass through, rather than one the caller assembles by hand.
	// The key the docs name, so a consumer reading .criteria[].remove_entity_id
	// gets the ID rather than null.
	var row struct {
		RemoveEntityID string `json:"remove_entity_id"`
	}
	if err := json.Unmarshal(res.Criteria[0], &row); err != nil {
		t.Fatalf("row is not JSON: %v", err)
	}
	if row.RemoveEntityID != "111~222" {
		t.Errorf("removeEntityId = %q, want 111~222", row.RemoveEntityID)
	}
	if err := json.Unmarshal(res.Criteria[1], &row); err != nil {
		t.Fatalf("row is not JSON: %v", err)
	}
	if row.RemoveEntityID != "111~333" {
		t.Errorf("removeEntityId = %q, want 111~333", row.RemoveEntityID)
	}
}

func TestCampaignCriteria_RemovalIDRendersAsAColumn(t *testing.T) {
	useTempState(t)
	srv := criteriaServer(t, nil, criteriaRows)
	defer srv.Close()
	c := newTestClient(t, srv)

	res, err := runCampaignCriteria(t.Context(), c, CampaignCriteriaArgs{CustomerID: "1", CampaignID: "111"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// The synthetic column has to resolve for --format table/csv too, not just
	// in the JSON payload.
	rows, fields := res.tableRows()
	if len(fields) == 0 || fields[len(fields)-1] != removeEntityIDField {
		t.Fatalf("fields = %v, want %s last", fields, removeEntityIDField)
	}
	table := formatTable(rows, fields)
	if !strings.Contains(table, "111~222") {
		t.Errorf("table output does not carry the removal ID:\n%s", table)
	}
}

func TestCampaignCriteria_FiltersByType(t *testing.T) {
	useTempState(t)
	var query string
	srv := criteriaServer(t, &query, criteriaRows)
	defer srv.Close()
	c := newTestClient(t, srv)

	if _, err := runCampaignCriteria(t.Context(), c, CampaignCriteriaArgs{CustomerID: "1", CampaignID: "111", Type: "location"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(query, "campaign_criterion.type = 'LOCATION'") {
		t.Errorf("type filter missing or not upper-cased: %s", query)
	}
}

func TestCampaignCriteria_RejectsUnsafeInput(t *testing.T) {
	useTempState(t)
	cases := map[string]CampaignCriteriaArgs{
		"non-numeric campaign": {CustomerID: "1", CampaignID: "1 OR 1=1"},
		"missing campaign":     {CustomerID: "1"},
		"quoted type":          {CustomerID: "1", CampaignID: "111", Type: "LOCATION' OR '1'='1"},
		"type with a space":    {CustomerID: "1", CampaignID: "111", Type: "AD SCHEDULE"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			// A nil client would panic on any request, so reaching the API at
			// all fails the test.
			if _, err := runCampaignCriteria(t.Context(), nil, args); err == nil {
				t.Error("expected the argument to be rejected")
			}
		})
	}
}

func TestEnrichRemoveEntityIDs_PassesThroughIncompleteRows(t *testing.T) {
	rows := []json.RawMessage{
		json.RawMessage(`{"campaign":{"id":"111"}}`),                 // no criterion ID
		json.RawMessage(`{"campaignCriterion":{"criterionId":"2"}}`), // no campaign ID
		json.RawMessage(`not json`),
	}
	for i, got := range enrichRemoveEntityIDs(rows) {
		if strings.Contains(string(got), removeEntityIDField) {
			t.Errorf("row %d gained a removal ID it cannot support: %s", i, got)
		}
	}
}

func TestRemoveEntity_RemovesACampaignCriterion(t *testing.T) {
	useTempState(t)
	srv, cap := mutateServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)

	args := EntityActionArgs{CustomerID: "1", EntityType: "campaign_criterion", EntityID: "111~222"}
	prev, err := runRemoveEntity(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	// remove_entity is destructive, so it takes two confirmations.
	args.Confirm = prev.Token
	second, err := runRemoveEntity(t.Context(), c, args)
	if err != nil {
		t.Fatalf("first confirm: %v", err)
	}
	args.Confirm = second.Token
	if _, err := runRemoveEntity(t.Context(), c, args); err != nil {
		t.Fatalf("second confirm: %v", err)
	}
	op, _ := cap.firstOp(t)["campaignCriterionOperation"].(map[string]any)
	if op == nil {
		t.Fatalf("operation is not a campaignCriterionOperation: %v", cap.firstOp(t))
	}
	if op["remove"] != "customers/1/campaignCriteria/111~222" {
		t.Errorf("remove = %v", op["remove"])
	}
}

func TestEntityResourceAndOp_CampaignCriterion(t *testing.T) {
	resource, opKey, err := entityResourceAndOp("1", "campaign_criterion", "111~222")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resource != "customers/1/campaignCriteria/111~222" || opKey != "campaignCriterionOperation" {
		t.Errorf("resource=%q op=%q", resource, opKey)
	}
	// A bare criterion ID previews fine but fails at confirm with an
	// invalid-resource-name error, so it is rejected up front — and the error
	// names the campaign half rather than an ad group.
	_, _, err = entityResourceAndOp("1", "campaign_criterion", "222")
	if err == nil || !strings.Contains(err.Error(), "campaignId~criterionId") {
		t.Fatalf("expected an error naming the composite shape, got %v", err)
	}
}
