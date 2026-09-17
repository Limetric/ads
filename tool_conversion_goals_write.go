package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// This file changes conversion goals. Account and campaign goals are created
// by Google whenever a conversion action introduces a new category and origin,
// so they can only be switched on or off for bidding. Custom goals are created,
// updated, and removed like any other resource, and a campaign's goal config
// chooses between the account goals, its own, and a custom goal.
//
// Google splits the owning account: account and custom goals are written on
// the conversion tracking account, campaign goals and goal configs on the
// campaign's own account.

// conversionOrigins are the ConversionOrigin values a goal can carry.
var conversionOrigins = map[string]bool{
	"APP": true, "CALL_FROM_ADS": true, "GOOGLE_HOSTED": true,
	"STORE": true, "WEBSITE": true, "YOUTUBE_HOSTED": true,
}

// goalKey is one category and origin pair, the identity of an account or
// campaign goal.
type goalKey struct{ Category, Origin string }

func (k goalKey) String() string { return k.Category + ":" + k.Origin }

// resourceID is the key as it appears in a goal resource name.
func (k goalKey) resourceID() string { return k.Category + "~" + k.Origin }

// parseGoalKey parses CATEGORY:ORIGIN, e.g. PURCHASE:WEBSITE.
func parseGoalKey(arg, s string) (goalKey, error) {
	category, origin, ok := strings.Cut(strings.ToUpper(strings.TrimSpace(s)), ":")
	if !ok || category == "" || origin == "" {
		return goalKey{}, fmt.Errorf("%s entry %q must be CATEGORY:ORIGIN, e.g. PURCHASE:WEBSITE", arg, s)
	}
	if !conversionActionCategories[category] {
		return goalKey{}, fmt.Errorf("%s entry %q has unknown category %q — use one of %s", arg, s, category, enumChoices(conversionActionCategories))
	}
	if !conversionOrigins[origin] {
		return goalKey{}, fmt.Errorf("%s entry %q has unknown origin %q — use one of %s", arg, s, origin, enumChoices(conversionOrigins))
	}
	return goalKey{category, origin}, nil
}

// goalChange is one requested biddable setting.
type goalChange struct {
	key      goalKey
	biddable bool
}

// parseGoalChanges turns the biddable and not_biddable lists into changes,
// refusing an empty request and a goal named twice.
func parseGoalChanges(biddable, notBiddable []string) ([]goalChange, error) {
	var changes []goalChange
	seen := map[goalKey]bool{}
	add := func(arg string, entries []string, value bool) error {
		for _, entry := range entries {
			key, err := parseGoalKey(arg, entry)
			if err != nil {
				return err
			}
			if seen[key] {
				return fmt.Errorf("goal %s is named more than once — list each goal in either biddable or not_biddable, once", key)
			}
			seen[key] = true
			changes = append(changes, goalChange{key, value})
		}
		return nil
	}
	if err := add("biddable", biddable, true); err != nil {
		return nil, err
	}
	if err := add("not_biddable", notBiddable, false); err != nil {
		return nil, err
	}
	if len(changes) == 0 {
		return nil, fmt.Errorf("no changes specified — pass goals as CATEGORY:ORIGIN in biddable and/or not_biddable")
	}
	return changes, nil
}

// goalStates decodes goal rows into their current biddable setting by key.
func goalStates(rows []json.RawMessage, resource string) (map[goalKey]bool, error) {
	states := make(map[goalKey]bool, len(rows))
	for _, raw := range rows {
		var row map[string]json.RawMessage
		if err := json.Unmarshal(raw, &row); err != nil {
			return nil, fmt.Errorf("decode %s row: %w", resource, err)
		}
		var goal struct {
			Category string `json:"category"`
			Origin   string `json:"origin"`
			Biddable bool   `json:"biddable"`
		}
		if err := json.Unmarshal(row[resource], &goal); err != nil {
			return nil, fmt.Errorf("decode %s row: %w", resource, err)
		}
		states[goalKey{goal.Category, goal.Origin}] = goal.Biddable
	}
	return states, nil
}

// describeGoalChanges checks every change against the goals that exist and
// renders the preview lines, refusing a request that changes nothing. A goal that does not exist cannot be created —
// Google makes one when a conversion action with that category and origin is
// added — so it fails here rather than at confirm.
func describeGoalChanges(changes []goalChange, current map[goalKey]bool, where string) ([]string, error) {
	var lines []string
	changed := false
	for _, ch := range changes {
		was, ok := current[ch.key]
		if !ok {
			return nil, fmt.Errorf("%s has no %s goal — Google creates a goal when a conversion action with that category and origin exists; list the goals with `ads google goals`", where, ch.key)
		}
		state := map[bool]string{true: "biddable", false: "not biddable"}
		if was == ch.biddable {
			lines = append(lines, fmt.Sprintf("%s stays %s", ch.key, state[ch.biddable]))
		} else {
			changed = true
			lines = append(lines, fmt.Sprintf("%s %s → %s", ch.key, state[was], state[ch.biddable]))
		}
	}
	if !changed {
		return nil, fmt.Errorf("nothing to change — %s", strings.Join(lines, "; "))
	}
	return lines, nil
}

// --- account goals ---

// UpdateAccountConversionGoalsArgs switches account-level goals on or off for
// bidding.
type UpdateAccountConversionGoalsArgs struct {
	CustomerID  string   `json:"customer_id,omitempty" jsonschema:"the Google Ads conversion tracking account; omit to use the configured default customer"`
	Biddable    []string `json:"biddable,omitempty" jsonschema:"goals to bid toward, each CATEGORY:ORIGIN, e.g. PURCHASE:WEBSITE"`
	NotBiddable []string `json:"not_biddable,omitempty" jsonschema:"goals to stop bidding toward, each CATEGORY:ORIGIN"`
	Confirm     string   `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

func runUpdateAccountConversionGoals(ctx context.Context, c *Client, args UpdateAccountConversionGoalsArgs) (WriteResult, error) {
	const tool = "update_account_conversion_goals"
	if err := checkBlockedOperation(tool, loadSafetyConfig()); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if args.Confirm != "" {
		return applyConfirmed(ctx, c, tool, args.Confirm)
	}
	changes, err := parseGoalChanges(args.Biddable, args.NotBiddable)
	if err != nil {
		return WriteResult{}, err
	}
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return WriteResult{}, err
	}
	if err := requireConversionCustomer(ctx, c, cid, "account conversion goals"); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	rows, err := c.Search(ctx, cid, "SELECT customer_conversion_goal.category, customer_conversion_goal.origin, "+
		"customer_conversion_goal.biddable FROM customer_conversion_goal")
	if err != nil {
		return WriteResult{}, toolError(tool, fmt.Errorf("look up account conversion goals: %w", err))
	}
	current, err := goalStates(rows, "customerConversionGoal")
	if err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	lines, err := describeGoalChanges(changes, current, "customer "+cid)
	if err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	ops := make([]any, 0, len(changes))
	for _, ch := range changes {
		ops = append(ops, map[string]any{"customerConversionGoalOperation": map[string]any{
			"update": map[string]any{
				"resourceName": fmt.Sprintf("customers/%s/customerConversionGoals/%s", cid, ch.key.resourceID()),
				"biddable":     ch.biddable,
			},
			"updateMask": "biddable",
		}})
	}
	// Account goals are what every campaign without its own goals bids toward,
	// so a change moves all of them at once and takes a second confirmation.
	summary := fmt.Sprintf("Update account conversion goals of customer %s: %s — this changes bidding in every campaign that follows the account goals",
		cid, strings.Join(lines, "; "))
	return previewMutateDouble(tool, cid, summary, ops)
}

// --- campaign goals ---

// UpdateCampaignConversionGoalsArgs switches one campaign's goals on or off
// for bidding.
type UpdateCampaignConversionGoalsArgs struct {
	CustomerID  string   `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID that owns the campaign; omit to use the configured default customer"`
	CampaignID  string   `json:"campaign_id" jsonschema:"the campaign whose goals to change"`
	Biddable    []string `json:"biddable,omitempty" jsonschema:"goals this campaign should bid toward, each CATEGORY:ORIGIN, e.g. PURCHASE:WEBSITE"`
	NotBiddable []string `json:"not_biddable,omitempty" jsonschema:"goals this campaign should stop bidding toward, each CATEGORY:ORIGIN"`
	Confirm     string   `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

// campaignGoalConfig is a campaign's conversion_goal_campaign_config.
type campaignGoalConfig struct {
	CampaignName         string
	GoalConfigLevel      string
	CustomConversionGoal string
}

// fetchCampaignGoalConfig resolves a campaign's goal config, which also
// proves the campaign exists in customerID.
func fetchCampaignGoalConfig(ctx context.Context, c *Client, customerID, campaignID string) (campaignGoalConfig, error) {
	rows, err := c.Search(ctx, customerID, "SELECT campaign.name, conversion_goal_campaign_config.goal_config_level, "+
		"conversion_goal_campaign_config.custom_conversion_goal FROM conversion_goal_campaign_config "+
		"WHERE campaign.id = "+campaignID)
	if err != nil {
		return campaignGoalConfig{}, fmt.Errorf("look up the goal settings of campaign %s: %w", campaignID, err)
	}
	if len(rows) == 0 {
		return campaignGoalConfig{}, fmt.Errorf("campaign %s was not found in customer %s", campaignID, customerID)
	}
	var row struct {
		Campaign struct {
			Name string `json:"name"`
		} `json:"campaign"`
		Config struct {
			GoalConfigLevel      string `json:"goalConfigLevel"`
			CustomConversionGoal string `json:"customConversionGoal"`
		} `json:"conversionGoalCampaignConfig"`
	}
	if err := json.Unmarshal(rows[0], &row); err != nil {
		return campaignGoalConfig{}, fmt.Errorf("decode the goal settings of campaign %s: %w", campaignID, err)
	}
	return campaignGoalConfig{
		CampaignName: row.Campaign.Name, GoalConfigLevel: row.Config.GoalConfigLevel,
		CustomConversionGoal: row.Config.CustomConversionGoal,
	}, nil
}

func runUpdateCampaignConversionGoals(ctx context.Context, c *Client, args UpdateCampaignConversionGoalsArgs) (WriteResult, error) {
	const tool = "update_campaign_conversion_goals"
	if err := checkBlockedOperation(tool, loadSafetyConfig()); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if args.Confirm != "" {
		return applyConfirmed(ctx, c, tool, args.Confirm)
	}
	campaignID, err := numericID("campaign_id", args.CampaignID)
	if err != nil {
		return WriteResult{}, err
	}
	changes, err := parseGoalChanges(args.Biddable, args.NotBiddable)
	if err != nil {
		return WriteResult{}, err
	}
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return WriteResult{}, err
	}
	config, err := fetchCampaignGoalConfig(ctx, c, cid, campaignID)
	if err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	rows, err := c.Search(ctx, cid, "SELECT campaign_conversion_goal.category, campaign_conversion_goal.origin, "+
		"campaign_conversion_goal.biddable FROM campaign_conversion_goal WHERE campaign.id = "+campaignID)
	if err != nil {
		return WriteResult{}, toolError(tool, fmt.Errorf("look up the conversion goals of campaign %s: %w", campaignID, err))
	}
	current, err := goalStates(rows, "campaignConversionGoal")
	if err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	lines, err := describeGoalChanges(changes, current, "campaign "+campaignID)
	if err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	ops := make([]any, 0, len(changes))
	for _, ch := range changes {
		ops = append(ops, map[string]any{"campaignConversionGoalOperation": map[string]any{
			"update": map[string]any{
				"resourceName": fmt.Sprintf("customers/%s/campaignConversionGoals/%s~%s", cid, campaignID, ch.key.resourceID()),
				"biddable":     ch.biddable,
			},
			"updateMask": "biddable",
		}})
	}
	summary := fmt.Sprintf("Update conversion goals of campaign %q (ID %s): %s", config.CampaignName, campaignID, strings.Join(lines, "; "))
	if config.CustomConversionGoal != "" {
		summary += " (the campaign also bids toward its custom goal " + config.CustomConversionGoal + ")"
	}
	if config.GoalConfigLevel != "CAMPAIGN" {
		summary += " — the campaign switches from the account goals to its own, and later account goal changes stop applying to it"
	}
	return previewMutate(tool, cid, summary, ops)
}

// --- campaign goal config ---

var goalConfigLevels = map[string]bool{"CUSTOMER": true, "CAMPAIGN": true}

// UpdateCampaignGoalConfigArgs chooses where a campaign takes its goals from.
type UpdateCampaignGoalConfigArgs struct {
	CustomerID      string `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID that owns the campaign; omit to use the configured default customer"`
	CampaignID      string `json:"campaign_id" jsonschema:"the campaign to configure"`
	GoalConfigLevel string `json:"goal_config_level,omitempty" jsonschema:"CUSTOMER to follow the account goals again (discards the campaign's own goal settings and custom goal), or CAMPAIGN to use its own goals"`
	CustomGoalID    string `json:"custom_goal_id,omitempty" jsonschema:"ID of a custom conversion goal for the campaign to bid toward"`
	ClearCustomGoal bool   `json:"clear_custom_goal,omitempty" jsonschema:"detach the campaign's custom conversion goal"`
	Confirm         string `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

func validateCampaignGoalConfig(args *UpdateCampaignGoalConfigArgs) error {
	level, err := normalizeEnum("goal_config_level", args.GoalConfigLevel, goalConfigLevels)
	if err != nil {
		return err
	}
	args.GoalConfigLevel = level
	if level == "" && args.CustomGoalID == "" && !args.ClearCustomGoal {
		return fmt.Errorf("no changes specified — pass goal_config_level, custom_goal_id, or clear_custom_goal")
	}
	if args.CustomGoalID != "" && args.ClearCustomGoal {
		return fmt.Errorf("custom_goal_id and clear_custom_goal contradict each other — pass one")
	}
	if level == "CUSTOMER" && (args.CustomGoalID != "" || args.ClearCustomGoal) {
		return fmt.Errorf("goal_config_level CUSTOMER already detaches the custom goal, and a campaign following the account goals cannot use one — pass goal_config_level on its own")
	}
	if args.CustomGoalID != "" {
		if _, err := numericID("custom_goal_id", args.CustomGoalID); err != nil {
			return err
		}
	}
	return nil
}

func runUpdateCampaignGoalConfig(ctx context.Context, c *Client, args UpdateCampaignGoalConfigArgs) (WriteResult, error) {
	const tool = "update_campaign_goal_config"
	if err := checkBlockedOperation(tool, loadSafetyConfig()); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if args.Confirm != "" {
		return applyConfirmed(ctx, c, tool, args.Confirm)
	}
	campaignID, err := numericID("campaign_id", args.CampaignID)
	if err != nil {
		return WriteResult{}, err
	}
	if err := validateCampaignGoalConfig(&args); err != nil {
		return WriteResult{}, err
	}
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return WriteResult{}, err
	}
	config, err := fetchCampaignGoalConfig(ctx, c, cid, campaignID)
	if err != nil {
		return WriteResult{}, toolError(tool, err)
	}

	update := map[string]any{"resourceName": fmt.Sprintf("customers/%s/conversionGoalCampaignConfigs/%s", cid, campaignID)}
	var mask, changes []string
	if args.GoalConfigLevel != "" {
		update["goalConfigLevel"] = args.GoalConfigLevel
		mask = append(mask, "goalConfigLevel")
		if args.GoalConfigLevel == "CUSTOMER" {
			changes = append(changes, "follow the account goals (discards its own goal settings and custom goal)")
		} else {
			changes = append(changes, "use campaign-level goals")
		}
	}
	if args.CustomGoalID != "" {
		// A custom goal can live on a conversion tracking manager, so its
		// resource name is taken from the lookup rather than built from cid.
		goal, err := fetchCustomGoal(ctx, c, cid, args.CustomGoalID)
		if err != nil {
			return WriteResult{}, toolError(tool, err)
		}
		update["customConversionGoal"] = goal.ResourceName
		mask = append(mask, "customConversionGoal")
		change := fmt.Sprintf("bid toward custom goal %q (ID %s)", goal.Name, goal.ID)
		if config.GoalConfigLevel != "CAMPAIGN" && args.GoalConfigLevel == "" {
			// Google moves a campaign that takes a custom goal to campaign-level
			// goals, so account goal changes stop reaching it.
			change += ", which switches it to campaign-level goals"
		}
		changes = append(changes, change)
	}
	if args.ClearCustomGoal {
		if config.CustomConversionGoal == "" {
			return WriteResult{}, toolError(tool, fmt.Errorf("campaign %s has no custom conversion goal to clear", campaignID))
		}
		// Masked but absent from update: the field-mask update clears it.
		mask = append(mask, "customConversionGoal")
		changes = append(changes, "detach custom goal "+config.CustomConversionGoal)
	}

	op := map[string]any{"conversionGoalCampaignConfigOperation": map[string]any{
		"update":     update,
		"updateMask": strings.Join(mask, ","),
	}}
	summary := fmt.Sprintf("Update goal settings of campaign %q (ID %s, currently %s): %s",
		config.CampaignName, campaignID, strings.ToLower(config.GoalConfigLevel)+"-level goals", strings.Join(changes, " and "))
	// Returning to the account goals throws away every campaign-specific goal
	// setting, which cannot be recovered afterwards.
	if args.GoalConfigLevel == "CUSTOMER" && config.GoalConfigLevel != "CUSTOMER" {
		return previewMutateDouble(tool, cid, summary, []any{op})
	}
	return previewMutate(tool, cid, summary, []any{op})
}

// --- custom goals ---

// customGoalInfo is what the custom goal tools resolve before staging.
type customGoalInfo struct {
	ID                string
	Name              string
	ResourceName      string
	ConversionActions []string
}

// fetchCustomGoal resolves a non-removed custom conversion goal visible from
// customerID.
func fetchCustomGoal(ctx context.Context, c *Client, customerID, goalID string) (customGoalInfo, error) {
	rows, err := c.Search(ctx, customerID, "SELECT custom_conversion_goal.resource_name, custom_conversion_goal.id, "+
		"custom_conversion_goal.name, custom_conversion_goal.status, custom_conversion_goal.conversion_actions "+
		"FROM custom_conversion_goal WHERE custom_conversion_goal.id = "+goalID)
	if err != nil {
		return customGoalInfo{}, fmt.Errorf("look up custom conversion goal %s: %w", goalID, err)
	}
	if len(rows) == 0 {
		return customGoalInfo{}, fmt.Errorf("custom conversion goal %s was not found in customer %s — list them with `ads google goals custom`", goalID, customerID)
	}
	var row struct {
		Goal struct {
			ResourceName      string   `json:"resourceName"`
			Name              string   `json:"name"`
			Status            string   `json:"status"`
			ConversionActions []string `json:"conversionActions"`
		} `json:"customConversionGoal"`
	}
	if err := json.Unmarshal(rows[0], &row); err != nil {
		return customGoalInfo{}, fmt.Errorf("decode custom conversion goal %s: %w", goalID, err)
	}
	if row.Goal.Status == "REMOVED" {
		return customGoalInfo{}, fmt.Errorf("custom conversion goal %s (%q) is removed", goalID, row.Goal.Name)
	}
	return customGoalInfo{ID: goalID, Name: row.Goal.Name, ResourceName: row.Goal.ResourceName, ConversionActions: row.Goal.ConversionActions}, nil
}

// resolveGoalConversionActions checks that every ID names a non-removed
// conversion action in customerID and returns their resource names in the
// order given.
func resolveGoalConversionActions(ctx context.Context, c *Client, customerID string, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("conversion_action_ids needs at least one conversion action ID")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if _, err := numericID("conversion_action_ids", id); err != nil {
			return nil, err
		}
		if seen[id] {
			return nil, fmt.Errorf("conversion action %s is listed more than once in conversion_action_ids", id)
		}
		seen[id] = true
	}
	rows, err := c.Search(ctx, customerID, fmt.Sprintf("SELECT conversion_action.id FROM conversion_action "+
		"WHERE conversion_action.id IN (%s) AND conversion_action.status != 'REMOVED'", strings.Join(ids, ", ")))
	if err != nil {
		return nil, fmt.Errorf("look up conversion actions: %w", err)
	}
	found := map[string]bool{}
	for _, raw := range rows {
		var row struct {
			ConversionAction struct {
				ID string `json:"id"`
			} `json:"conversionAction"`
		}
		if err := json.Unmarshal(raw, &row); err != nil {
			return nil, fmt.Errorf("decode conversion action: %w", err)
		}
		found[row.ConversionAction.ID] = true
	}
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		if !found[id] {
			return nil, fmt.Errorf("conversion action %s was not found (or is removed) in customer %s — list them with `ads google conversions`", id, customerID)
		}
		names = append(names, fmt.Sprintf("customers/%s/conversionActions/%s", customerID, id))
	}
	return names, nil
}

// CreateCustomConversionGoalArgs creates a custom conversion goal.
type CreateCustomConversionGoalArgs struct {
	CustomerID          string   `json:"customer_id,omitempty" jsonschema:"the Google Ads conversion tracking account; omit to use the configured default customer"`
	Name                string   `json:"name" jsonschema:"custom goal name"`
	ConversionActionIDs []string `json:"conversion_action_ids" jsonschema:"IDs of the conversion actions the goal bids toward"`
	Confirm             string   `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

func runCreateCustomConversionGoal(ctx context.Context, c *Client, args CreateCustomConversionGoalArgs) (WriteResult, error) {
	const tool = "create_custom_conversion_goal"
	if err := checkBlockedOperation(tool, loadSafetyConfig()); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if args.Confirm != "" {
		return applyConfirmed(ctx, c, tool, args.Confirm)
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return WriteResult{}, fmt.Errorf("name is required")
	}
	if len(args.ConversionActionIDs) == 0 {
		return WriteResult{}, fmt.Errorf("conversion_action_ids needs at least one conversion action ID")
	}
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return WriteResult{}, err
	}
	if err := requireConversionCustomer(ctx, c, cid, "custom conversion goals"); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	actions, err := resolveGoalConversionActions(ctx, c, cid, args.ConversionActionIDs)
	if err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	op := map[string]any{"customConversionGoalOperation": map[string]any{"create": map[string]any{
		"name": name, "conversionActions": actions, "status": "ENABLED",
	}}}
	summary := fmt.Sprintf("Create custom conversion goal %q bidding toward conversion actions %s — attach it to a campaign with update_campaign_goal_config",
		name, strings.Join(args.ConversionActionIDs, ", "))
	return previewMutate(tool, cid, summary, []any{op})
}

// UpdateCustomConversionGoalArgs renames a custom goal or replaces its
// conversion actions.
type UpdateCustomConversionGoalArgs struct {
	CustomerID          string   `json:"customer_id,omitempty" jsonschema:"the Google Ads conversion tracking account; omit to use the configured default customer"`
	GoalID              string   `json:"goal_id" jsonschema:"ID of the custom conversion goal"`
	Name                string   `json:"name,omitempty" jsonschema:"a new name"`
	ConversionActionIDs []string `json:"conversion_action_ids,omitempty" jsonschema:"the complete new set of conversion action IDs (replaces the current set)"`
	Confirm             string   `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

func runUpdateCustomConversionGoal(ctx context.Context, c *Client, args UpdateCustomConversionGoalArgs) (WriteResult, error) {
	const tool = "update_custom_conversion_goal"
	if err := checkBlockedOperation(tool, loadSafetyConfig()); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if args.Confirm != "" {
		return applyConfirmed(ctx, c, tool, args.Confirm)
	}
	goalID, err := numericID("goal_id", args.GoalID)
	if err != nil {
		return WriteResult{}, err
	}
	name := strings.TrimSpace(args.Name)
	if name == "" && len(args.ConversionActionIDs) == 0 {
		return WriteResult{}, fmt.Errorf("no changes specified — pass name and/or conversion_action_ids")
	}
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return WriteResult{}, err
	}
	if err := requireConversionCustomer(ctx, c, cid, "custom conversion goals"); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	goal, err := fetchCustomGoal(ctx, c, cid, goalID)
	if err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	update := map[string]any{"resourceName": fmt.Sprintf("customers/%s/customConversionGoals/%s", cid, goalID)}
	var mask, changes []string
	if name != "" {
		update["name"] = name
		mask = append(mask, "name")
		changes = append(changes, fmt.Sprintf("rename to %q", name))
	}
	if len(args.ConversionActionIDs) > 0 {
		actions, err := resolveGoalConversionActions(ctx, c, cid, args.ConversionActionIDs)
		if err != nil {
			return WriteResult{}, toolError(tool, err)
		}
		update["conversionActions"] = actions
		mask = append(mask, "conversionActions")
		changes = append(changes, fmt.Sprintf("bid toward conversion actions %s (was %d action(s))", strings.Join(args.ConversionActionIDs, ", "), len(goal.ConversionActions)))
	}
	op := map[string]any{"customConversionGoalOperation": map[string]any{
		"update":     update,
		"updateMask": strings.Join(mask, ","),
	}}
	summary := fmt.Sprintf("Update custom conversion goal %q (ID %s): %s", goal.Name, goalID, strings.Join(changes, " and "))
	if len(args.ConversionActionIDs) > 0 {
		// Every campaign using the goal starts bidding toward the new set.
		return previewMutateDouble(tool, cid, summary+" — this changes bidding in every campaign using the goal", []any{op})
	}
	return previewMutate(tool, cid, summary, []any{op})
}

// RemoveCustomConversionGoalArgs removes a custom conversion goal.
type RemoveCustomConversionGoalArgs struct {
	CustomerID string `json:"customer_id,omitempty" jsonschema:"the Google Ads conversion tracking account; omit to use the configured default customer"`
	GoalID     string `json:"goal_id" jsonschema:"ID of the custom conversion goal to remove"`
	Confirm    string `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

func runRemoveCustomConversionGoal(ctx context.Context, c *Client, args RemoveCustomConversionGoalArgs) (WriteResult, error) {
	const tool = "remove_custom_conversion_goal"
	if err := checkBlockedOperation(tool, loadSafetyConfig()); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if args.Confirm != "" {
		return applyConfirmed(ctx, c, tool, args.Confirm)
	}
	goalID, err := numericID("goal_id", args.GoalID)
	if err != nil {
		return WriteResult{}, err
	}
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return WriteResult{}, err
	}
	if err := requireConversionCustomer(ctx, c, cid, "custom conversion goals"); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	goal, err := fetchCustomGoal(ctx, c, cid, goalID)
	if err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	op := map[string]any{"customConversionGoalOperation": map[string]any{
		"remove": fmt.Sprintf("customers/%s/customConversionGoals/%s", cid, goalID),
	}}
	// The tool name marks this destructive: two confirmations.
	summary := fmt.Sprintf("Remove custom conversion goal %q (ID %s)", goal.Name, goalID)
	return previewMutate(tool, cid, summary, []any{op})
}

// --- CLI front-end ---

var (
	setAccountGoalsArgs  UpdateAccountConversionGoalsArgs
	setCampaignGoalsArgs UpdateCampaignConversionGoalsArgs
	campaignGoalCfgArgs  UpdateCampaignGoalConfigArgs
	createCustomGoalArgs CreateCustomConversionGoalArgs
	updateCustomGoalArgs UpdateCustomConversionGoalArgs
	removeCustomGoalArgs RemoveCustomConversionGoalArgs
)

var goalCmd = &cobra.Command{
	Use:   "goal",
	Short: "Change conversion goals: account and campaign biddable goals, campaign goal source, custom goals",
}

// goalWriteCmd builds a preview-then-confirm goal subcommand around run.
func goalWriteCmd(use, short string, run func(context.Context, *Client) (WriteResult, error)) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := newGoogleClient(cmd.Context())
			if err != nil {
				return err
			}
			res, err := run(cmd.Context(), client)
			if err != nil {
				return err
			}
			return printJSON(cmd.OutOrStdout(), res)
		},
	}
}

func init() {
	setAccount := goalWriteCmd("set-account", "Make account conversion goals biddable or not (previews first; two confirmations required)",
		func(ctx context.Context, c *Client) (WriteResult, error) {
			return runUpdateAccountConversionGoals(ctx, c, setAccountGoalsArgs)
		})
	f := setAccount.Flags()
	f.StringVar(&setAccountGoalsArgs.CustomerID, "customer-id", "", "Google Ads conversion tracking account (falls back to the configured default)")
	f.StringSliceVar(&setAccountGoalsArgs.Biddable, "biddable", nil, "goal to bid toward, CATEGORY:ORIGIN (repeatable)")
	f.StringSliceVar(&setAccountGoalsArgs.NotBiddable, "not-biddable", nil, "goal to stop bidding toward, CATEGORY:ORIGIN (repeatable)")
	f.StringVar(&setAccountGoalsArgs.Confirm, "confirm", "", "confirm token from a previous preview")

	setCampaign := goalWriteCmd("set-campaign", "Make a campaign's conversion goals biddable or not (previews first; --confirm to apply)",
		func(ctx context.Context, c *Client) (WriteResult, error) {
			return runUpdateCampaignConversionGoals(ctx, c, setCampaignGoalsArgs)
		})
	f = setCampaign.Flags()
	f.StringVar(&setCampaignGoalsArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	f.StringVar(&setCampaignGoalsArgs.CampaignID, "campaign-id", "", "campaign ID (required)")
	f.StringSliceVar(&setCampaignGoalsArgs.Biddable, "biddable", nil, "goal to bid toward, CATEGORY:ORIGIN (repeatable)")
	f.StringSliceVar(&setCampaignGoalsArgs.NotBiddable, "not-biddable", nil, "goal to stop bidding toward, CATEGORY:ORIGIN (repeatable)")
	f.StringVar(&setCampaignGoalsArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = setCampaign.MarkFlagRequired("campaign-id")

	campaignConfig := goalWriteCmd("campaign-config", "Choose a campaign's goal source: account goals, its own, or a custom goal (previews first; --confirm to apply)",
		func(ctx context.Context, c *Client) (WriteResult, error) {
			return runUpdateCampaignGoalConfig(ctx, c, campaignGoalCfgArgs)
		})
	f = campaignConfig.Flags()
	f.StringVar(&campaignGoalCfgArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	f.StringVar(&campaignGoalCfgArgs.CampaignID, "campaign-id", "", "campaign ID (required)")
	f.StringVar(&campaignGoalCfgArgs.GoalConfigLevel, "level", "", "CUSTOMER (follow account goals) or CAMPAIGN")
	f.StringVar(&campaignGoalCfgArgs.CustomGoalID, "custom-goal-id", "", "custom conversion goal to bid toward")
	f.BoolVar(&campaignGoalCfgArgs.ClearCustomGoal, "clear-custom-goal", false, "detach the campaign's custom conversion goal")
	f.StringVar(&campaignGoalCfgArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = campaignConfig.MarkFlagRequired("campaign-id")

	createCustom := goalWriteCmd("create-custom", "Create a custom conversion goal (previews first; --confirm to apply)",
		func(ctx context.Context, c *Client) (WriteResult, error) {
			return runCreateCustomConversionGoal(ctx, c, createCustomGoalArgs)
		})
	f = createCustom.Flags()
	f.StringVar(&createCustomGoalArgs.CustomerID, "customer-id", "", "Google Ads conversion tracking account (falls back to the configured default)")
	f.StringVar(&createCustomGoalArgs.Name, "name", "", "custom goal name (required)")
	f.StringSliceVar(&createCustomGoalArgs.ConversionActionIDs, "conversion-action-id", nil, "conversion action ID (repeatable, required)")
	f.StringVar(&createCustomGoalArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = createCustom.MarkFlagRequired("name")
	_ = createCustom.MarkFlagRequired("conversion-action-id")

	updateCustom := goalWriteCmd("update-custom", "Rename a custom conversion goal or replace its conversion actions (previews first; --confirm to apply)",
		func(ctx context.Context, c *Client) (WriteResult, error) {
			return runUpdateCustomConversionGoal(ctx, c, updateCustomGoalArgs)
		})
	f = updateCustom.Flags()
	f.StringVar(&updateCustomGoalArgs.CustomerID, "customer-id", "", "Google Ads conversion tracking account (falls back to the configured default)")
	f.StringVar(&updateCustomGoalArgs.GoalID, "goal-id", "", "custom conversion goal ID (required)")
	f.StringVar(&updateCustomGoalArgs.Name, "name", "", "new name")
	f.StringSliceVar(&updateCustomGoalArgs.ConversionActionIDs, "conversion-action-id", nil, "conversion action ID of the new complete set (repeatable)")
	f.StringVar(&updateCustomGoalArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = updateCustom.MarkFlagRequired("goal-id")

	removeCustom := goalWriteCmd("remove-custom", "Remove a custom conversion goal (previews first; two confirmations required)",
		func(ctx context.Context, c *Client) (WriteResult, error) {
			return runRemoveCustomConversionGoal(ctx, c, removeCustomGoalArgs)
		})
	f = removeCustom.Flags()
	f.StringVar(&removeCustomGoalArgs.CustomerID, "customer-id", "", "Google Ads conversion tracking account (falls back to the configured default)")
	f.StringVar(&removeCustomGoalArgs.GoalID, "goal-id", "", "custom conversion goal ID (required)")
	f.StringVar(&removeCustomGoalArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = removeCustom.MarkFlagRequired("goal-id")

	goalCmd.AddCommand(setAccount, setCampaign, campaignConfig, createCustom, updateCustom, removeCustom)
}
