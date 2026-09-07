package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

func parseEUPoliticalAds(value string) (string, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "":
		return "", nil
	case "DOES-NOT-CONTAIN", "DOES_NOT_CONTAIN_EU_POLITICAL_ADVERTISING":
		return "DOES_NOT_CONTAIN_EU_POLITICAL_ADVERTISING", nil
	case "CONTAINS", "CONTAINS_EU_POLITICAL_ADVERTISING":
		return "CONTAINS_EU_POLITICAL_ADVERTISING", nil
	default:
		return "", fmt.Errorf("eu_political_ads must be does-not-contain or contains, got %q", value)
	}
}

// A declaration must already be persisted before geo changes are staged. A
// combined declaration/geo batch is not a repair: Google's account-level gate
// exempts only updates that touch the declaration field alone.
func preflightCampaignPoliticalAds(ctx context.Context, c *Client, customerID, campaignID string) error {
	if c == nil {
		return fmt.Errorf("cannot check EU political ads declaration: Google Ads client is unavailable")
	}
	query := fmt.Sprintf("SELECT campaign.id, campaign.contains_eu_political_advertising FROM campaign WHERE campaign.id = %s", campaignID)
	rows, err := c.Search(ctx, customerID, query)
	if err != nil {
		return fmt.Errorf("check campaign %s EU political ads declaration: %w", campaignID, err)
	}
	if len(rows) == 0 {
		return fmt.Errorf("cannot check EU political ads declaration: campaign %s was not found", campaignID)
	}
	var row struct {
		Campaign struct {
			Declaration string `json:"containsEuPoliticalAdvertising"`
		} `json:"campaign"`
	}
	if err := json.Unmarshal(rows[0], &row); err != nil {
		return fmt.Errorf("decode campaign %s EU political ads declaration: %w", campaignID, err)
	}
	switch row.Campaign.Declaration {
	case "CONTAINS_EU_POLITICAL_ADVERTISING", "DOES_NOT_CONTAIN_EU_POLITICAL_ADVERTISING":
		return nil
	default:
		return fmt.Errorf("campaign %s has no resolved per-campaign EU political ads declaration (contains_eu_political_advertising); geo changes require it regardless of target country or account-level declaration. Run ads google campaign update --customer-id %s --campaign-id %s --eu-political-ads does-not-contain (or contains if accurate) by itself, confirm its token with ads confirm, then retry the geo update", campaignID, customerID, campaignID)
	}
}
