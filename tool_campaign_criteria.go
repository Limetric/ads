package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// This file lists a campaign's criteria — the geo targets, languages,
// ad-schedule windows, and negative keywords that hang off a campaign. Criteria
// are addressed for removal by campaignId~criterionId, and a criterion ID is
// minted by Google rather than supplied by the caller, so without this the only
// way to find one was raw GAQL (issue #52).

// validCriterionTypeToken bounds the campaign_criterion.type filter to an
// enum-shaped token. The value is interpolated into GAQL, and Google's enum is
// long enough that an allow-list here would go stale; a token that cannot
// contain a quote, a space, or an operator cannot escape the literal either
// (issues #8, #13).
func validCriterionTypeToken(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
		case i > 0 && (r == '_' || (r >= '0' && r <= '9')):
		default:
			return false
		}
	}
	return true
}

// CampaignCriteriaArgs lists the criteria attached to one campaign.
type CampaignCriteriaArgs struct {
	CustomerID string `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID that owns the campaign; omit to use the configured default customer"`
	CampaignID string `json:"campaign_id" jsonschema:"the campaign ID whose criteria to list"`
	Type       string `json:"type,omitempty" jsonschema:"optional campaign_criterion.type filter, e.g. LOCATION, LANGUAGE, AD_SCHEDULE, or KEYWORD"`
}

type CampaignCriteriaResult struct {
	Criteria   []json.RawMessage `json:"criteria"`
	TotalCount int               `json:"total_count"`
	// selectFields carries the SELECT column order for the CLI's --format
	// table/csv rendering; unexported so JSON/MCP output is unchanged.
	selectFields []string
}

func (r CampaignCriteriaResult) tableRows() ([]json.RawMessage, []string) {
	return r.Criteria, r.selectFields
}

func runCampaignCriteria(ctx context.Context, c *Client, args CampaignCriteriaArgs) (CampaignCriteriaResult, error) {
	const tool = "campaign_criteria"
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return CampaignCriteriaResult{}, err
	}
	campaignID, err := numericID("campaign_id", args.CampaignID)
	if err != nil {
		return CampaignCriteriaResult{}, err
	}
	where := fmt.Sprintf("campaign.id = %s AND campaign_criterion.status != 'REMOVED'", campaignID)
	if filter := strings.ToUpper(strings.TrimSpace(args.Type)); filter != "" {
		if !validCriterionTypeToken(filter) {
			return CampaignCriteriaResult{}, fmt.Errorf("type %q is not a campaign_criterion.type value — pass an enum name such as LOCATION, LANGUAGE, AD_SCHEDULE, or KEYWORD", args.Type)
		}
		where += fmt.Sprintf(" AND campaign_criterion.type = '%s'", filter)
	}
	query, err := buildSelect([]string{
		"campaign.id",
		"campaign_criterion.criterion_id",
		"campaign_criterion.type",
		"campaign_criterion.negative",
		"campaign_criterion.status",
		"campaign_criterion.location.geo_target_constant",
		"campaign_criterion.language.language_constant",
		"campaign_criterion.keyword.text",
		"campaign_criterion.keyword.match_type",
		"campaign_criterion.ad_schedule.day_of_week",
		"campaign_criterion.ad_schedule.start_hour",
		"campaign_criterion.ad_schedule.start_minute",
		"campaign_criterion.ad_schedule.end_hour",
		"campaign_criterion.ad_schedule.end_minute",
	}, "campaign_criterion", where, 0)
	if err != nil {
		return CampaignCriteriaResult{}, err
	}
	rows, err := c.Search(ctx, cid, query)
	if err != nil {
		return CampaignCriteriaResult{}, toolError(tool, err)
	}
	rows = enrichRemoveEntityIDs(rows)
	fields := append(parseSelectFields(query), removeEntityIDField)
	return CampaignCriteriaResult{Criteria: rows, TotalCount: len(rows), selectFields: fields}, nil
}

// removeEntityIDField is the synthetic column enrichRemoveEntityIDs adds, and
// the key it is emitted under. resolveField looks a field path up verbatim
// before trying its camelCase form, so the one name serves both the JSON
// payload and the table/CSV column.
const removeEntityIDField = "remove_entity_id"

// enrichRemoveEntityIDs adds the composite campaignId~criterionId that
// remove_entity takes, so a criterion found here can be removed without the
// caller assembling the ID by hand. Rows missing either half, or that fail to
// decode, are passed through unchanged.
func enrichRemoveEntityIDs(rows []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, len(rows))
	for i, r := range rows {
		out[i] = r
		v, ok := decodeRow(r)
		if !ok {
			continue
		}
		row, ok := v.(map[string]any)
		if !ok {
			continue
		}
		campaignID := resolveField(row, "campaign.id")
		criterionID := resolveField(row, "campaign_criterion.criterion_id")
		if campaignID == "" || criterionID == "" {
			continue
		}
		// Under the documented snake_case name, not the camelCase the Google
		// fields around it use: this is our column, and docs, table output, and
		// a JSON consumer must all be able to name the same key.
		row[removeEntityIDField] = campaignID + "~" + criterionID
		if enriched, err := json.Marshal(row); err == nil {
			out[i] = enriched
		}
	}
	return out
}

// --- CLI front-end ---

var (
	campaignCriteriaArgs   CampaignCriteriaArgs
	campaignCriteriaFormat string
)

var campaignCriteriaCmd = &cobra.Command{
	Use:   "criteria",
	Short: "List a campaign's criteria (geo, language, ad schedule, negatives) with the IDs remove takes",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runCampaignCriteria(cmd.Context(), client, campaignCriteriaArgs)
		if err != nil {
			return err
		}
		return printResult(cmd.OutOrStdout(), campaignCriteriaFormat, res)
	},
}

func init() {
	f := campaignCriteriaCmd.Flags()
	f.StringVar(&campaignCriteriaArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	f.StringVar(&campaignCriteriaArgs.CampaignID, "campaign-id", "", "campaign ID (required)")
	f.StringVar(&campaignCriteriaArgs.Type, "type", "", "campaign_criterion.type filter, e.g. LOCATION, LANGUAGE, AD_SCHEDULE, KEYWORD")
	f.StringVar(&campaignCriteriaFormat, "format", "json", "output format: json, table, or csv")
	_ = campaignCriteriaCmd.MarkFlagRequired("campaign-id")

	campaignCmd.AddCommand(campaignCriteriaCmd)
}
