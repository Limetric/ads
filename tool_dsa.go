package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// Dynamic Search Ads — the write half of a feature the read path could already
// see in full (issue #60). A DSA campaign is a Search campaign carrying a
// dynamic_search_ads_setting; its ad groups are SEARCH_DYNAMIC_ADS, they hold
// expanded dynamic search ads (descriptions only — Google generates the
// headline and the final URL from the crawled page), and they match against
// webpage criteria instead of keywords. Every piece is inert without the other
// three, so all four land together.
//
// This file owns the shared DSA builders plus the two DSA-only tools; the
// campaign setting and the ad group type are set by the campaign/ad group
// tools that already exist.

// Ad group types this binary can create. The enum has many more, but each one
// belongs to a campaign type with its own creation path (Shopping, Video,
// Hotel, …); offering them here would stage ad groups Google rejects.
const (
	adGroupTypeSearchStandard  = "SEARCH_STANDARD"
	adGroupTypeSearchDynamic   = "SEARCH_DYNAMIC_ADS"
	adGroupTypeDisplayStandard = "DISPLAY_STANDARD"
)

// parseAdGroupType validates a caller-supplied ad group type
// (case-insensitive). Type is immutable once an ad group exists, so a wrong
// value here cannot be corrected later — it fails at preview instead.
func parseAdGroupType(s string) (string, error) {
	switch v := strings.ToUpper(strings.TrimSpace(s)); v {
	case adGroupTypeSearchStandard, adGroupTypeSearchDynamic, adGroupTypeDisplayStandard:
		return v, nil
	default:
		return "", fmt.Errorf("unsupported ad group type %q — use SEARCH_STANDARD, SEARCH_DYNAMIC_ADS (Dynamic Search Ads), or DISPLAY_STANDARD; other types belong to campaign kinds with their own creation tools", s)
	}
}

// dynamicSearchAdsSetting builds campaign.dynamic_search_ads_setting from the
// DSA arguments, plus the field-mask leaves naming it (creates ignore the
// mask). It returns a nil map when the campaign is not a DSA campaign, so an
// omitted setting stays untouched.
//
// domain_name and language_code are both REQUIRED on the setting, so they are
// demanded together: a setting carrying one of the two is rejected by Google
// at confirm time, which is the failure preview exists to catch (issue #14).
// use_supplied_urls_only travels with them rather than being masked on its
// own, so the setting is always written as one coherent whole.
//
// The leaves are masked rather than the message: dynamicSearchAdsSetting has
// defined sub-fields, so it cannot appear bare in a field mask — the same rule
// geoTargetTypeSetting follows.
func dynamicSearchAdsSetting(domain, languageCode string, useSuppliedURLsOnly bool) (map[string]any, []string, error) {
	domain = strings.TrimSpace(domain)
	languageCode = strings.TrimSpace(languageCode)
	if domain == "" && languageCode == "" {
		if useSuppliedURLsOnly {
			return nil, nil, fmt.Errorf("dsa_use_supplied_urls_only only applies to a Dynamic Search Ads campaign — pass dsa_domain and dsa_language_code as well, or drop the flag")
		}
		return nil, nil, nil
	}
	if domain == "" {
		return nil, nil, fmt.Errorf("dsa_domain is required alongside dsa_language_code — Google Ads requires both on a dynamic_search_ads_setting")
	}
	if languageCode == "" {
		return nil, nil, fmt.Errorf("dsa_language_code is required alongside dsa_domain — Google Ads requires both on a dynamic_search_ads_setting (e.g. en)")
	}
	if err := validateDSADomain(domain); err != nil {
		return nil, nil, err
	}
	setting := map[string]any{
		"domainName":          domain,
		"languageCode":        languageCode,
		"useSuppliedUrlsOnly": useSuppliedURLsOnly,
	}
	mask := []string{
		"dynamicSearchAdsSetting.domainName",
		"dynamicSearchAdsSetting.languageCode",
		"dynamicSearchAdsSetting.useSuppliedUrlsOnly",
	}
	return setting, mask, nil
}

// validateDSADomain rejects a domain written as a URL. Google wants the bare
// Internet domain name ("example.com", "www.example.com"); a scheme or a path
// is accepted by the preview and rejected by the API, so it is caught here.
func validateDSADomain(domain string) error {
	if strings.Contains(domain, "://") || strings.ContainsAny(domain, "/ ") {
		return fmt.Errorf("dsa_domain %q must be a bare domain name, not a URL — pass e.g. example.com or www.example.com", domain)
	}
	return nil
}

// dsaCampaignSummary describes the DSA half of a campaign preview summary, or
// "" when the campaign is not a DSA campaign.
func dsaCampaignSummary(setting map[string]any) string {
	if setting == nil {
		return ""
	}
	s := fmt.Sprintf("dynamic search ads for %v (%v)", setting["domainName"], setting["languageCode"])
	if only, _ := setting["useSuppliedUrlsOnly"].(bool); only {
		s += ", supplied URLs only"
	}
	return s
}

// --- Webpage criteria (dynamic ad targets) ---

// webpageConditionOperands are the operands a dynamic ad target can match on.
// Each condition names one; the conditions on a target are AND-ed, so a target
// matches a page only when every one of them holds.
var webpageConditionOperands = map[string]bool{
	"URL":          true,
	"CATEGORY":     true,
	"PAGE_TITLE":   true,
	"PAGE_CONTENT": true,
	"CUSTOM_LABEL": true,
}

// WebpageCondition is one condition of a dynamic ad target.
//
// WebpageConditionInfo also carries an operator (EQUALS/CONTAINS), which is
// deliberately not exposed: Google's own DSA sample omits it, and CATEGORY and
// CUSTOM_LABEL accept only EQUALS. Adding it later is additive.
type WebpageCondition struct {
	Operand  string `json:"operand" jsonschema:"URL, CATEGORY, PAGE_TITLE, PAGE_CONTENT, or CUSTOM_LABEL"`
	Argument string `json:"argument" jsonschema:"the value the operand is matched against, e.g. /specialoffers for a URL condition or a category name from the domain_category resource"`
}

// WebpageTarget is one dynamic ad target: a named set of conditions selecting
// the part of the campaign's site a dynamic ad group advertises.
type WebpageTarget struct {
	CriterionName string             `json:"criterion_name" jsonschema:"a name for the target; Google requires one and uses it to identify and sort dynamic ad targets"`
	Conditions    []WebpageCondition `json:"conditions,omitempty" jsonschema:"the conditions a page must ALL match; omit them and set all_webpages to target the whole site"`
	AllWebpages   bool               `json:"all_webpages,omitempty" jsonschema:"target every page of the campaign's domain; mutually exclusive with conditions"`
	CpcBidMicros  int64              `json:"cpc_bid_micros,omitempty" jsonschema:"optional CPC bid in micros for this target, overriding the ad group default"`
}

// validate checks one target before it is staged.
func (t WebpageTarget) validate(index int) error {
	label := fmt.Sprintf("target %d", index+1)
	if strings.TrimSpace(t.CriterionName) == "" {
		return fmt.Errorf("%s: criterion_name is required — Google Ads requires a name on every dynamic ad target", label)
	}
	// An empty condition list is what Google reads as "every page of the
	// domain", so it can never be the result of forgetting a condition: the
	// whole site is opted into explicitly (issue #60).
	if t.AllWebpages && len(t.Conditions) > 0 {
		return fmt.Errorf("%s (%q): all_webpages cannot be combined with conditions — pass all_webpages alone to target the whole site, or conditions alone to target part of it", label, t.CriterionName)
	}
	if !t.AllWebpages && len(t.Conditions) == 0 {
		return fmt.Errorf("%s (%q): at least one condition is required — pass e.g. URL=/specialoffers, or set all_webpages to target every page of the domain", label, t.CriterionName)
	}
	if t.CpcBidMicros < 0 {
		return fmt.Errorf("%s (%q): cpc_bid_micros must be positive (micros), got %d", label, t.CriterionName, t.CpcBidMicros)
	}
	for i, cond := range t.Conditions {
		operand := strings.ToUpper(strings.TrimSpace(cond.Operand))
		if !webpageConditionOperands[operand] {
			return fmt.Errorf("%s (%q) condition %d: unsupported operand %q — use URL, CATEGORY, PAGE_TITLE, PAGE_CONTENT, or CUSTOM_LABEL", label, t.CriterionName, i+1, cond.Operand)
		}
		if strings.TrimSpace(cond.Argument) == "" {
			return fmt.Errorf("%s (%q) condition %d: %s needs an argument to match against", label, t.CriterionName, i+1, operand)
		}
	}
	return nil
}

// criterion builds the ad_group_criterion create payload for one target.
func (t WebpageTarget) criterion(adGroupResource string) map[string]any {
	conditions := make([]any, 0, len(t.Conditions))
	for _, cond := range t.Conditions {
		conditions = append(conditions, map[string]any{
			"operand": strings.ToUpper(strings.TrimSpace(cond.Operand)),
			// Trimmed because Google matches the argument literally: a
			// condition written with natural spacing ("URL = /sale") would
			// otherwise stage a target that matches nothing.
			"argument": strings.TrimSpace(cond.Argument),
		})
	}
	create := map[string]any{
		"adGroup": adGroupResource,
		// An empty conditions array is meaningful, not missing: it targets
		// every page of the campaign's domain. It is written out so the
		// preview shows what was asked for.
		"webpage": map[string]any{
			"criterionName": strings.TrimSpace(t.CriterionName),
			"conditions":    conditions,
		},
	}
	if t.CpcBidMicros != 0 {
		create["cpcBidMicros"] = microsString(t.CpcBidMicros)
	}
	return map[string]any{"adGroupCriterionOperation": map[string]any{"create": create}}
}

// describe renders one target for the preview summary.
func (t WebpageTarget) describe() string {
	name := strings.TrimSpace(t.CriterionName)
	if t.AllWebpages {
		return fmt.Sprintf("%q (all webpages)", name)
	}
	parts := make([]string, 0, len(t.Conditions))
	for _, cond := range t.Conditions {
		parts = append(parts, fmt.Sprintf("%s=%s", strings.ToUpper(strings.TrimSpace(cond.Operand)), strings.TrimSpace(cond.Argument)))
	}
	return fmt.Sprintf("%q (%s)", name, strings.Join(parts, " AND "))
}

// AddWebpageTargetsArgs adds dynamic ad targets (webpage criteria) to a
// dynamic ad group. Without targets a dynamic ad group has nothing to match
// against, so this is the step that makes a DSA ad group serve.
type AddWebpageTargetsArgs struct {
	CustomerID string          `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID that owns the ad group; omit to use the configured default customer"`
	AdGroupID  string          `json:"ad_group_id" jsonschema:"the SEARCH_DYNAMIC_ADS ad group ID to add dynamic ad targets to"`
	Targets    []WebpageTarget `json:"targets" jsonschema:"the dynamic ad targets to add"`
	Confirm    string          `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

func runAddWebpageTargets(ctx context.Context, c *Client, args AddWebpageTargetsArgs) (WriteResult, error) {
	const tool = "add_webpage_targets"
	if err := checkBlockedOperation(tool, loadSafetyConfig()); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if len(args.Targets) == 0 {
		return WriteResult{}, fmt.Errorf("at least one dynamic ad target is required")
	}
	for i, target := range args.Targets {
		if err := target.validate(i); err != nil {
			return WriteResult{}, err
		}
	}
	if args.Confirm != "" {
		return applyConfirmed(ctx, c, tool, args.Confirm)
	}
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return WriteResult{}, err
	}
	adGroupID, err := numericID("ad_group_id", args.AdGroupID)
	if err != nil {
		return WriteResult{}, err
	}
	if err := requireDynamicAdGroup(ctx, c, cid, adGroupID, "dynamic ad targets"); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	adGroupResource := fmt.Sprintf("customers/%s/adGroups/%s", cid, adGroupID)
	ops := make([]any, len(args.Targets))
	described := make([]string, len(args.Targets))
	for i, target := range args.Targets {
		ops[i] = target.criterion(adGroupResource)
		described[i] = target.describe()
	}
	summary := fmt.Sprintf("Add %d dynamic ad target(s) to ad group %s: %s",
		len(args.Targets), adGroupID, strings.Join(described, ", "))
	return previewMutate(tool, cid, summary, ops)
}

// --- Expanded Dynamic Search Ads ---

// DraftDsaArgs drafts an expanded Dynamic Search Ad. A DSA carries only
// descriptions: Google generates the headline from the search query and the
// final URL from the landing page it crawled, so there is nothing here to
// match draft_responsive_search_ad's headlines and final_url.
type DraftDsaArgs struct {
	CustomerID   string `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID that owns the ad group; omit to use the configured default customer"`
	AdGroupID    string `json:"ad_group_id" jsonschema:"the SEARCH_DYNAMIC_ADS ad group ID to create the ad in"`
	Description  string `json:"description" jsonschema:"the ad description, at most 90 characters"`
	Description2 string `json:"description2,omitempty" jsonschema:"an optional second description, at most 90 characters"`
	Status       string `json:"status,omitempty" jsonschema:"ENABLED or PAUSED; defaults to PAUSED"`
	Confirm      string `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

func runDraftDynamicSearchAd(ctx context.Context, c *Client, args DraftDsaArgs) (WriteResult, error) {
	const tool = "draft_dynamic_search_ad"
	if err := checkBlockedOperation(tool, loadSafetyConfig()); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if strings.TrimSpace(args.Description) == "" {
		return WriteResult{}, fmt.Errorf("description is required — a dynamic search ad supplies only its descriptions; Google generates the headline and the final URL from the crawled page")
	}
	if err := validateDescription(args.Description); err != nil {
		return WriteResult{}, err
	}
	if args.Description2 != "" {
		if err := validateDescription(args.Description2); err != nil {
			return WriteResult{}, err
		}
	}
	status, err := parseCreateStatus(args.Status)
	if err != nil {
		return WriteResult{}, err
	}
	if args.Confirm != "" {
		return applyConfirmed(ctx, c, tool, args.Confirm)
	}
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return WriteResult{}, err
	}
	adGroupID, err := numericID("ad_group_id", args.AdGroupID)
	if err != nil {
		return WriteResult{}, err
	}
	if err := requireDynamicAdGroup(ctx, c, cid, adGroupID, "dynamic search ads"); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	dsa := map[string]any{"description": args.Description}
	if args.Description2 != "" {
		dsa["description2"] = args.Description2
	}
	op := map[string]any{
		"adGroupAdOperation": map[string]any{
			"create": map[string]any{
				"adGroup": fmt.Sprintf("customers/%s/adGroups/%s", cid, adGroupID),
				// No finalUrls: a DSA's landing page is chosen by Google from
				// the pages its dynamic ad targets match.
				"ad":     map[string]any{"expandedDynamicSearchAd": dsa},
				"status": string(status),
			},
		},
	}
	descriptions := 1
	if args.Description2 != "" {
		descriptions = 2
	}
	summary := fmt.Sprintf("Draft dynamic search ad in ad group %s (%d description(s), status %s)",
		adGroupID, descriptions, status)
	res, err := previewMutate(tool, cid, summary, []any{op})
	if err != nil {
		return WriteResult{}, err
	}
	return res.withCreateStatus(status, enableAdHint(adGroupID, "<resolve ad_id from apply response>")), nil
}

// --- Shared ad group type guard ---

// requireDynamicAdGroup rejects a DSA write aimed at an ad group that is not a
// dynamic one, naming what was being added.
//
// Unlike the BROAD+MANUAL_CPC guard in tool_keywords_write.go, a lookup that
// fails does not block the write: this guard only turns a confirm-time API
// rejection into a preview-time error, and no spend rides on it, so a read
// that cannot answer leaves the decision to Google's own validation rather
// than making the tool unusable.
func requireDynamicAdGroup(ctx context.Context, c *Client, customerID, adGroupID, what string) error {
	actual, err := adGroupTypeOf(ctx, c, customerID, adGroupID)
	if err != nil || actual == "" || actual == adGroupTypeSearchDynamic {
		return nil
	}
	return fmt.Errorf("ad group %s is a %s ad group, which cannot hold %s — they need a SEARCH_DYNAMIC_ADS ad group, and an ad group's type is fixed at creation; create a new one with create_ad_group (type SEARCH_DYNAMIC_ADS) in a campaign that carries a dynamic search ads setting",
		adGroupID, actual, what)
}

// adGroupTypeOf looks up an ad group's type. "" with a nil error means the
// type could not be resolved (no client, no rows, or an unexpected shape).
func adGroupTypeOf(ctx context.Context, c *Client, customerID, adGroupID string) (string, error) {
	if c == nil {
		return "", nil
	}
	q := fmt.Sprintf("SELECT ad_group.type FROM ad_group WHERE ad_group.id = %s", adGroupID)
	rows, err := c.Search(ctx, customerID, q)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", nil
	}
	var row struct {
		AdGroup struct {
			Type string `json:"type"`
		} `json:"adGroup"`
	}
	if json.Unmarshal(rows[0], &row) != nil {
		return "", nil
	}
	return row.AdGroup.Type, nil
}

// --- CLI front-end ---

var (
	draftDsaArgs          DraftDsaArgs
	addWebpageTargetsArgs AddWebpageTargetsArgs
	webpageTarget         WebpageTarget
	webpageConditionFlags []string
)

// parseWebpageConditionFlag parses "OPERAND=ARGUMENT" (e.g. URL=/specialoffers).
// The split is on the first "=" so an argument may contain more of them, as a
// URL with a query string does.
func parseWebpageConditionFlag(v string) WebpageCondition {
	operand, argument, found := strings.Cut(v, "=")
	if !found {
		// Left whole so the validator names the unusable operand rather than
		// silently targeting something else.
		return WebpageCondition{Operand: v}
	}
	return WebpageCondition{Operand: strings.ToUpper(strings.TrimSpace(operand)), Argument: argument}
}

var adDraftDsaCmd = &cobra.Command{
	Use:   "draft-dsa",
	Short: "Draft a dynamic search ad in a dynamic ad group (previews first; --confirm to apply)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runDraftDynamicSearchAd(cmd.Context(), client, draftDsaArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

var webpageTargetsCmd = &cobra.Command{
	Use:   "webpage-targets",
	Short: "Manage dynamic ad targets on Dynamic Search Ads ad groups",
}

// The CLI adds one target per invocation; the tool's Args carry a list so an
// MCP caller can stage a whole target set in a single confirmed batch.
var webpageTargetsAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add a dynamic ad target to a dynamic ad group (previews first; --confirm to apply)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		for _, s := range webpageConditionFlags {
			webpageTarget.Conditions = append(webpageTarget.Conditions, parseWebpageConditionFlag(s))
		}
		addWebpageTargetsArgs.Targets = []WebpageTarget{webpageTarget}
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runAddWebpageTargets(cmd.Context(), client, addWebpageTargetsArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

func init() {
	f := adDraftDsaCmd.Flags()
	f.StringVar(&draftDsaArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	f.StringVar(&draftDsaArgs.AdGroupID, "ad-group-id", "", "dynamic ad group ID (required)")
	f.StringVar(&draftDsaArgs.Description, "description", "", "ad description, at most 90 characters (required)")
	f.StringVar(&draftDsaArgs.Description2, "description2", "", "optional second description, at most 90 characters")
	f.StringVar(&draftDsaArgs.Status, "status", "", "ENABLED or PAUSED (default)")
	f.StringVar(&draftDsaArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = adDraftDsaCmd.MarkFlagRequired("ad-group-id")
	adCmd.AddCommand(adDraftDsaCmd)

	tf := webpageTargetsAddCmd.Flags()
	tf.StringVar(&addWebpageTargetsArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	tf.StringVar(&addWebpageTargetsArgs.AdGroupID, "ad-group-id", "", "dynamic ad group ID (required)")
	tf.StringVar(&webpageTarget.CriterionName, "criterion-name", "", "name for the dynamic ad target (required)")
	tf.StringArrayVar(&webpageConditionFlags, "condition", nil, "target condition as OPERAND=ARGUMENT, e.g. URL=/specialoffers (repeatable; conditions are AND-ed)")
	tf.BoolVar(&webpageTarget.AllWebpages, "all-webpages", false, "target every page of the campaign's domain (mutually exclusive with --condition)")
	tf.Int64Var(&webpageTarget.CpcBidMicros, "cpc-bid-micros", 0, "CPC bid in micros for this target, overriding the ad group default")
	tf.StringVar(&addWebpageTargetsArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = webpageTargetsAddCmd.MarkFlagRequired("ad-group-id")

	webpageTargetsCmd.AddCommand(webpageTargetsAddCmd)
}
