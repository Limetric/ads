package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// This file creates, updates, and removes conversion actions — the tracking
// half of conversion goals. Listing lives in tool_conversions.go; the goals
// Google groups these actions into live in tool_conversion_goals*.go.
//
// Conversion actions belong to the account's conversion tracking customer, so
// every write here checks that it is running against that account before
// staging, rather than failing at confirm.

// creatableConversionActionTypes are the ConversionActionType values the API
// accepts on create. The rest are read only, deprecated, or created by linking
// another product (Firebase, Google Analytics, third-party app analytics).
var creatableConversionActionTypes = map[string]bool{
	"AD_CALL": true, "CLICK_TO_CALL": true, "GOOGLE_PLAY_DOWNLOAD": true,
	"GOOGLE_PLAY_IN_APP_PURCHASE": true, "STORE_SALES_DIRECT_UPLOAD": true,
	"UPLOAD_CALLS": true, "UPLOAD_CLICKS": true, "WEBPAGE": true, "WEBSITE_CALL": true,
}

// conversionActionCategories are the settable ConversionActionCategory values.
var conversionActionCategories = map[string]bool{
	"ADD_TO_CART": true, "BEGIN_CHECKOUT": true, "BOOK_APPOINTMENT": true, "CONTACT": true,
	"CONVERTED_LEAD": true, "DEFAULT": true, "DOWNLOAD": true, "ENGAGEMENT": true,
	"GET_DIRECTIONS": true, "IMPORTED_LEAD": true, "OUTBOUND_CLICK": true, "PAGE_VIEW": true,
	"PHONE_CALL_LEAD": true, "PURCHASE": true, "QUALIFIED_LEAD": true, "REQUEST_QUOTE": true,
	"SIGNUP": true, "STORE_SALE": true, "STORE_VISIT": true, "SUBMIT_LEAD_FORM": true,
	"SUBSCRIBE_PAID": true, "YOUTUBE_FOLLOW_ON_VIEWS": true,
}

var conversionCountingTypes = map[string]bool{"ONE_PER_CLICK": true, "MANY_PER_CLICK": true}

// conversionAttributionModels are the models Google still accepts on a write;
// the rule-based models (first click, linear, time decay, position based) were
// retired and are read only.
var conversionAttributionModels = map[string]bool{
	"GOOGLE_SEARCH_ATTRIBUTION_DATA_DRIVEN": true, "GOOGLE_ADS_LAST_CLICK": true,
}

// enumChoices renders an allow-list for an error message, sorted so the
// message is stable.
func enumChoices(set map[string]bool) string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// normalizeEnum upper-cases an enum argument and checks it against set. An
// empty value passes through as empty.
func normalizeEnum(arg, value string, set map[string]bool) (string, error) {
	v := strings.ToUpper(strings.TrimSpace(value))
	if v == "" {
		return "", nil
	}
	if !set[v] {
		return "", fmt.Errorf("unsupported %s %q — use one of %s", arg, value, enumChoices(set))
	}
	return v, nil
}

// ConversionActionSettings are the conversion action fields that can be set on
// create and changed on update. Pointers distinguish "not given" from a zero
// value that is meaningful (a default value of 0, primary_for_goal false).
type ConversionActionSettings struct {
	Category                 string   `json:"category,omitempty" jsonschema:"conversion category, e.g. PURCHASE, SIGNUP, SUBMIT_LEAD_FORM, PAGE_VIEW, DEFAULT; with the action's origin this decides which conversion goal it belongs to"`
	CountingType             string   `json:"counting_type,omitempty" jsonschema:"ONE_PER_CLICK (leads) or MANY_PER_CLICK (purchases)"`
	PrimaryForGoal           *bool    `json:"primary_for_goal,omitempty" jsonschema:"true (Google's default) makes this a primary action: counted in Conversions and used for bidding wherever its goal is biddable; false makes it secondary (All conversions only)"`
	DefaultValue             *float64 `json:"default_value,omitempty" jsonschema:"value in currency units recorded for a conversion that reports none"`
	CurrencyCode             string   `json:"currency_code,omitempty" jsonschema:"ISO 4217 currency of default_value, e.g. USD"`
	AlwaysUseDefaultValue    *bool    `json:"always_use_default_value,omitempty" jsonschema:"true to ignore reported values and always record default_value"`
	ClickThroughLookbackDays int64    `json:"click_through_lookback_window_days,omitempty" jsonschema:"days after a click a conversion can still count, 1-90"`
	ViewThroughLookbackDays  int64    `json:"view_through_lookback_window_days,omitempty" jsonschema:"days after an impression a view-through conversion can still count, 1-30"`
	AttributionModel         string   `json:"attribution_model,omitempty" jsonschema:"GOOGLE_SEARCH_ATTRIBUTION_DATA_DRIVEN or GOOGLE_ADS_LAST_CLICK"`
	PhoneCallDurationSeconds *int64   `json:"phone_call_duration_seconds,omitempty" jsonschema:"for call conversion types: minimum call length in seconds that counts, 0-10000"`
}

// build validates the settings and writes the given ones into action,
// recording each touched leaf in mask and describing it in changes. Enum
// arguments are normalized in place.
func (s *ConversionActionSettings) build(action map[string]any, mask, changes *[]string) error {
	set := func(leaf, change string) {
		*mask = append(*mask, leaf)
		*changes = append(*changes, change)
	}
	var err error
	if s.Category, err = normalizeEnum("category", s.Category, conversionActionCategories); err != nil {
		return err
	}
	if s.CountingType, err = normalizeEnum("counting_type", s.CountingType, conversionCountingTypes); err != nil {
		return err
	}
	if s.AttributionModel, err = normalizeEnum("attribution_model", s.AttributionModel, conversionAttributionModels); err != nil {
		return err
	}
	if s.ClickThroughLookbackDays != 0 && (s.ClickThroughLookbackDays < 1 || s.ClickThroughLookbackDays > 90) {
		return fmt.Errorf("click_through_lookback_window_days must be between 1 and 90, got %d", s.ClickThroughLookbackDays)
	}
	if s.ViewThroughLookbackDays != 0 && (s.ViewThroughLookbackDays < 1 || s.ViewThroughLookbackDays > 30) {
		return fmt.Errorf("view_through_lookback_window_days must be between 1 and 30, got %d", s.ViewThroughLookbackDays)
	}
	if s.PhoneCallDurationSeconds != nil && (*s.PhoneCallDurationSeconds < 0 || *s.PhoneCallDurationSeconds > 10000) {
		return fmt.Errorf("phone_call_duration_seconds must be between 0 and 10000, got %d", *s.PhoneCallDurationSeconds)
	}
	if s.DefaultValue != nil && *s.DefaultValue < 0 {
		return fmt.Errorf("default_value cannot be negative, got %v", *s.DefaultValue)
	}
	s.CurrencyCode = strings.ToUpper(strings.TrimSpace(s.CurrencyCode))
	if s.CurrencyCode != "" && !isCurrencyCode(s.CurrencyCode) {
		return fmt.Errorf("currency_code %q must be a three-letter ISO 4217 code such as USD or EUR", s.CurrencyCode)
	}

	if s.Category != "" {
		action["category"] = s.Category
		set("category", "category "+s.Category)
	}
	if s.CountingType != "" {
		action["countingType"] = s.CountingType
		set("countingType", "counting "+s.CountingType)
	}
	if s.PrimaryForGoal != nil {
		action["primaryForGoal"] = *s.PrimaryForGoal
		if *s.PrimaryForGoal {
			set("primaryForGoal", "primary action (used for bidding)")
		} else {
			set("primaryForGoal", "secondary action (not used for bidding)")
		}
	}
	values := map[string]any{}
	if s.DefaultValue != nil {
		values["defaultValue"] = *s.DefaultValue
		set("valueSettings.defaultValue", fmt.Sprintf("default value %v", *s.DefaultValue))
	}
	if s.CurrencyCode != "" {
		values["defaultCurrencyCode"] = s.CurrencyCode
		set("valueSettings.defaultCurrencyCode", "currency "+s.CurrencyCode)
	}
	if s.AlwaysUseDefaultValue != nil {
		values["alwaysUseDefaultValue"] = *s.AlwaysUseDefaultValue
		set("valueSettings.alwaysUseDefaultValue", fmt.Sprintf("always use default value %t", *s.AlwaysUseDefaultValue))
	}
	if len(values) > 0 {
		action["valueSettings"] = values
	}
	if s.ClickThroughLookbackDays != 0 {
		action["clickThroughLookbackWindowDays"] = strconv.FormatInt(s.ClickThroughLookbackDays, 10)
		set("clickThroughLookbackWindowDays", fmt.Sprintf("click-through window %d days", s.ClickThroughLookbackDays))
	}
	if s.ViewThroughLookbackDays != 0 {
		action["viewThroughLookbackWindowDays"] = strconv.FormatInt(s.ViewThroughLookbackDays, 10)
		set("viewThroughLookbackWindowDays", fmt.Sprintf("view-through window %d days", s.ViewThroughLookbackDays))
	}
	if s.AttributionModel != "" {
		action["attributionModelSettings"] = map[string]any{"attributionModel": s.AttributionModel}
		set("attributionModelSettings.attributionModel", "attribution "+s.AttributionModel)
	}
	if s.PhoneCallDurationSeconds != nil {
		action["phoneCallDurationSeconds"] = strconv.FormatInt(*s.PhoneCallDurationSeconds, 10)
		set("phoneCallDurationSeconds", fmt.Sprintf("minimum call length %ds", *s.PhoneCallDurationSeconds))
	}
	return nil
}

func isCurrencyCode(s string) bool {
	if len(s) != 3 {
		return false
	}
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// conversionActionInfo is what the update and remove tools resolve about an
// existing conversion action before staging.
type conversionActionInfo struct {
	ID            string
	Name          string
	Type          string
	Status        string
	Category      string
	Origin        string
	OwnerCustomer string
}

func (a conversionActionInfo) describe() string {
	return fmt.Sprintf("%q (%s, ID %s)", a.Name, a.Type, a.ID)
}

// fetchConversionAction resolves a conversion action and checks that
// customerID can change it: it must exist, not be removed, and be owned by
// customerID rather than by Google or a conversion tracking manager.
func fetchConversionAction(ctx context.Context, c *Client, customerID, actionID string) (conversionActionInfo, error) {
	q := fmt.Sprintf("SELECT conversion_action.id, conversion_action.name, conversion_action.type, "+
		"conversion_action.status, conversion_action.category, conversion_action.origin, "+
		"conversion_action.owner_customer FROM conversion_action WHERE conversion_action.id = %s", actionID)
	rows, err := c.Search(ctx, customerID, q)
	if err != nil {
		return conversionActionInfo{}, fmt.Errorf("look up conversion action %s: %w", actionID, err)
	}
	if len(rows) == 0 {
		return conversionActionInfo{}, fmt.Errorf("conversion action %s was not found in customer %s — list them with `ads google conversions`", actionID, customerID)
	}
	var row struct {
		ConversionAction struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			Type          string `json:"type"`
			Status        string `json:"status"`
			Category      string `json:"category"`
			Origin        string `json:"origin"`
			OwnerCustomer string `json:"ownerCustomer"`
		} `json:"conversionAction"`
	}
	if err := json.Unmarshal(rows[0], &row); err != nil {
		return conversionActionInfo{}, fmt.Errorf("decode conversion action %s: %w", actionID, err)
	}
	ca := row.ConversionAction
	info := conversionActionInfo{
		ID: actionID, Name: ca.Name, Type: ca.Type, Status: ca.Status, Category: ca.Category,
		Origin: ca.Origin, OwnerCustomer: strings.TrimPrefix(ca.OwnerCustomer, "customers/"),
	}
	if info.Status == "REMOVED" {
		return info, fmt.Errorf("conversion action %s %s is already removed", actionID, info.describe())
	}
	if info.OwnerCustomer == "" {
		return info, fmt.Errorf("conversion action %s is system-defined by Google and cannot be changed", info.describe())
	}
	if info.OwnerCustomer != customerID {
		return info, fmt.Errorf("conversion action %s is owned by customer %s, not %s — re-run with customer_id %s", info.describe(), info.OwnerCustomer, customerID, info.OwnerCustomer)
	}
	return info, nil
}

// --- create ---

// CreateConversionActionArgs creates a conversion action.
type CreateConversionActionArgs struct {
	CustomerID string `json:"customer_id,omitempty" jsonschema:"the Google Ads conversion tracking account to create the action in; omit to use the configured default customer"`
	Name       string `json:"name" jsonschema:"conversion action name, unique within the account"`
	Type       string `json:"type" jsonschema:"how conversions are measured: WEBPAGE (Google tag), UPLOAD_CLICKS (offline click import), UPLOAD_CALLS, AD_CALL, WEBSITE_CALL, CLICK_TO_CALL, GOOGLE_PLAY_DOWNLOAD, GOOGLE_PLAY_IN_APP_PURCHASE, or STORE_SALES_DIRECT_UPLOAD; cannot be changed later"`
	AppID      string `json:"app_id,omitempty" jsonschema:"the Android package name (e.g. com.example.app); required for GOOGLE_PLAY_DOWNLOAD and GOOGLE_PLAY_IN_APP_PURCHASE"`
	ConversionActionSettings
	Confirm string `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

func runCreateConversionAction(ctx context.Context, c *Client, args CreateConversionActionArgs) (WriteResult, error) {
	const tool = "create_conversion_action"
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
	actionType := strings.ToUpper(strings.TrimSpace(args.Type))
	if actionType == "" {
		return WriteResult{}, fmt.Errorf("type is required — use one of %s", enumChoices(creatableConversionActionTypes))
	}
	if !creatableConversionActionTypes[actionType] {
		return WriteResult{}, fmt.Errorf("type %q cannot be created through the API — use one of %s (Firebase, Google Analytics, and third-party app conversions are created by linking that product in Google Ads)", args.Type, enumChoices(creatableConversionActionTypes))
	}
	action := map[string]any{"name": name, "type": actionType, "status": "ENABLED"}
	appID := strings.TrimSpace(args.AppID)
	if strings.HasPrefix(actionType, "GOOGLE_PLAY_") {
		if appID == "" {
			return WriteResult{}, fmt.Errorf("app_id is required for %s — pass the app's Android package name, e.g. com.example.app", actionType)
		}
		action["appId"] = appID
	} else if appID != "" {
		return WriteResult{}, fmt.Errorf("app_id only applies to GOOGLE_PLAY_DOWNLOAD and GOOGLE_PLAY_IN_APP_PURCHASE, not %s", actionType)
	}
	var mask, changes []string
	if err := args.ConversionActionSettings.build(action, &mask, &changes); err != nil {
		return WriteResult{}, err
	}
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return WriteResult{}, err
	}
	if err := requireConversionCustomer(ctx, c, cid, "conversion actions"); err != nil {
		return WriteResult{}, toolError(tool, err)
	}

	summary := fmt.Sprintf("Create %s conversion action %q", actionType, name)
	if appID != "" {
		summary += " for app " + appID
	}
	if len(changes) > 0 {
		summary += ": " + strings.Join(changes, ", ")
	}
	if args.PrimaryForGoal == nil || *args.PrimaryForGoal {
		summary += " — as a primary action it counts in Conversions and drives bidding in every campaign whose goals include its category"
	}
	op := map[string]any{"conversionActionOperation": map[string]any{"create": action}}
	return previewMutate(tool, cid, summary, []any{op})
}

// --- update ---

// UpdateConversionActionArgs changes an existing conversion action.
type UpdateConversionActionArgs struct {
	CustomerID         string `json:"customer_id,omitempty" jsonschema:"the Google Ads account that owns the conversion action; omit to use the configured default customer"`
	ConversionActionID string `json:"conversion_action_id" jsonschema:"ID of the conversion action to update"`
	Name               string `json:"name,omitempty" jsonschema:"a new name"`
	ConversionActionSettings
	Confirm string `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

func runUpdateConversionAction(ctx context.Context, c *Client, args UpdateConversionActionArgs) (WriteResult, error) {
	const tool = "update_conversion_action"
	if err := checkBlockedOperation(tool, loadSafetyConfig()); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if args.Confirm != "" {
		return applyConfirmed(ctx, c, tool, args.Confirm)
	}
	actionID, err := numericID("conversion_action_id", args.ConversionActionID)
	if err != nil {
		return WriteResult{}, err
	}
	update := map[string]any{}
	var mask, changes []string
	if name := strings.TrimSpace(args.Name); name != "" {
		update["name"] = name
		mask = append(mask, "name")
		changes = append(changes, fmt.Sprintf("rename to %q", name))
	}
	if err := args.ConversionActionSettings.build(update, &mask, &changes); err != nil {
		return WriteResult{}, err
	}
	if len(mask) == 0 {
		return WriteResult{}, fmt.Errorf("no changes specified — pass name, category, counting_type, primary_for_goal, default_value, currency_code, always_use_default_value, a lookback window, attribution_model, or phone_call_duration_seconds")
	}
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return WriteResult{}, err
	}
	action, err := fetchConversionAction(ctx, c, cid, actionID)
	if err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	update["resourceName"] = fmt.Sprintf("customers/%s/conversionActions/%s", cid, actionID)

	op := map[string]any{"conversionActionOperation": map[string]any{
		"update":     update,
		"updateMask": strings.Join(mask, ","),
	}}
	summary := fmt.Sprintf("Update conversion action %s: %s", action.describe(), strings.Join(changes, ", "))
	// Category and primary_for_goal decide which goal the action belongs to and
	// whether it bids at all, so changing either moves bidding in every campaign
	// optimizing toward that goal. That takes a second confirmation, as a
	// portfolio target change does; renames and value settings do not.
	if (args.Category != "" && args.Category != action.Category) || args.PrimaryForGoal != nil {
		return previewMutateDouble(tool, cid, summary+" — this changes which conversions campaigns bid toward", []any{op})
	}
	return previewMutate(tool, cid, summary, []any{op})
}

// --- remove ---

// RemoveConversionActionArgs removes a conversion action.
type RemoveConversionActionArgs struct {
	CustomerID         string `json:"customer_id,omitempty" jsonschema:"the Google Ads account that owns the conversion action; omit to use the configured default customer"`
	ConversionActionID string `json:"conversion_action_id" jsonschema:"ID of the conversion action to remove"`
	Confirm            string `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

func runRemoveConversionAction(ctx context.Context, c *Client, args RemoveConversionActionArgs) (WriteResult, error) {
	const tool = "remove_conversion_action"
	if err := checkBlockedOperation(tool, loadSafetyConfig()); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if args.Confirm != "" {
		return applyConfirmed(ctx, c, tool, args.Confirm)
	}
	actionID, err := numericID("conversion_action_id", args.ConversionActionID)
	if err != nil {
		return WriteResult{}, err
	}
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return WriteResult{}, err
	}
	action, err := fetchConversionAction(ctx, c, cid, actionID)
	if err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	op := map[string]any{"conversionActionOperation": map[string]any{
		"remove": fmt.Sprintf("customers/%s/conversionActions/%s", cid, actionID),
	}}
	// The tool name marks this destructive, so staging requires two
	// confirmations (requiresDoubleConfirmation).
	summary := fmt.Sprintf("Remove conversion action %s — it stops recording conversions, and campaigns bidding toward it lose that signal", action.describe())
	return previewMutate(tool, cid, summary, []any{op})
}

// --- CLI front-end ---

// conversionSettingFlags holds the CLI values behind the optional pointer
// settings, which are only copied into the args when their flag was given.
type conversionSettingFlags struct {
	primary, alwaysDefault bool
	defaultValue           float64
	callSeconds            int64
}

func addConversionSettingFlags(cmd *cobra.Command, s *ConversionActionSettings, v *conversionSettingFlags) {
	f := cmd.Flags()
	f.StringVar(&s.Category, "category", "", "conversion category, e.g. PURCHASE, SIGNUP, SUBMIT_LEAD_FORM, DEFAULT")
	f.StringVar(&s.CountingType, "counting-type", "", "ONE_PER_CLICK or MANY_PER_CLICK")
	f.BoolVar(&v.primary, "primary", true, "primary action used for bidding, or --primary=false for secondary (omit to leave unset)")
	f.Float64Var(&v.defaultValue, "default-value", 0, "value recorded for a conversion that reports none")
	f.StringVar(&s.CurrencyCode, "currency", "", "ISO 4217 currency of the default value")
	f.BoolVar(&v.alwaysDefault, "always-use-default-value", false, "ignore reported values and always record the default")
	f.Int64Var(&s.ClickThroughLookbackDays, "click-through-days", 0, "click-through lookback window, 1-90 days")
	f.Int64Var(&s.ViewThroughLookbackDays, "view-through-days", 0, "view-through lookback window, 1-30 days")
	f.StringVar(&s.AttributionModel, "attribution-model", "", "GOOGLE_SEARCH_ATTRIBUTION_DATA_DRIVEN or GOOGLE_ADS_LAST_CLICK")
	f.Int64Var(&v.callSeconds, "call-duration-seconds", 0, "minimum call length that counts, 0-10000")
}

// applyConversionSettingFlags copies the optional settings whose flags were
// set into s.
func applyConversionSettingFlags(cmd *cobra.Command, s *ConversionActionSettings, v *conversionSettingFlags) {
	f := cmd.Flags()
	if f.Changed("primary") {
		s.PrimaryForGoal = &v.primary
	}
	if f.Changed("default-value") {
		s.DefaultValue = &v.defaultValue
	}
	if f.Changed("always-use-default-value") {
		s.AlwaysUseDefaultValue = &v.alwaysDefault
	}
	if f.Changed("call-duration-seconds") {
		s.PhoneCallDurationSeconds = &v.callSeconds
	}
}

var (
	createConversionArgs  CreateConversionActionArgs
	createConversionFlags conversionSettingFlags
	updateConversionArgs  UpdateConversionActionArgs
	updateConversionFlags conversionSettingFlags
	removeConversionArgs  RemoveConversionActionArgs
)

var conversionCmd = &cobra.Command{
	Use:   "conversion",
	Short: "Create, update, and remove conversion actions",
}

var conversionCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a conversion action (previews first; --confirm to apply)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		applyConversionSettingFlags(cmd, &createConversionArgs.ConversionActionSettings, &createConversionFlags)
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runCreateConversionAction(cmd.Context(), client, createConversionArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

var conversionUpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update a conversion action (previews first; --confirm to apply)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		applyConversionSettingFlags(cmd, &updateConversionArgs.ConversionActionSettings, &updateConversionFlags)
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runUpdateConversionAction(cmd.Context(), client, updateConversionArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

var conversionRemoveCmd = &cobra.Command{
	Use:   "remove",
	Short: "Remove a conversion action (previews first; two confirmations required)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runRemoveConversionAction(cmd.Context(), client, removeConversionArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

func init() {
	cf := conversionCreateCmd.Flags()
	cf.StringVar(&createConversionArgs.CustomerID, "customer-id", "", "Google Ads conversion tracking account (falls back to the configured default)")
	cf.StringVar(&createConversionArgs.Name, "name", "", "conversion action name (required)")
	cf.StringVar(&createConversionArgs.Type, "type", "", "WEBPAGE, UPLOAD_CLICKS, UPLOAD_CALLS, AD_CALL, WEBSITE_CALL, CLICK_TO_CALL, ... (required)")
	cf.StringVar(&createConversionArgs.AppID, "app-id", "", "Android package name (required for GOOGLE_PLAY_* types)")
	addConversionSettingFlags(conversionCreateCmd, &createConversionArgs.ConversionActionSettings, &createConversionFlags)
	cf.StringVar(&createConversionArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = conversionCreateCmd.MarkFlagRequired("name")
	_ = conversionCreateCmd.MarkFlagRequired("type")

	uf := conversionUpdateCmd.Flags()
	uf.StringVar(&updateConversionArgs.CustomerID, "customer-id", "", "Google Ads account that owns the action (falls back to the configured default)")
	uf.StringVar(&updateConversionArgs.ConversionActionID, "conversion-action-id", "", "conversion action ID (required)")
	uf.StringVar(&updateConversionArgs.Name, "name", "", "new name")
	addConversionSettingFlags(conversionUpdateCmd, &updateConversionArgs.ConversionActionSettings, &updateConversionFlags)
	uf.StringVar(&updateConversionArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = conversionUpdateCmd.MarkFlagRequired("conversion-action-id")

	rf := conversionRemoveCmd.Flags()
	rf.StringVar(&removeConversionArgs.CustomerID, "customer-id", "", "Google Ads account that owns the action (falls back to the configured default)")
	rf.StringVar(&removeConversionArgs.ConversionActionID, "conversion-action-id", "", "conversion action ID (required)")
	rf.StringVar(&removeConversionArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = conversionRemoveCmd.MarkFlagRequired("conversion-action-id")

	conversionCmd.AddCommand(conversionCreateCmd, conversionUpdateCmd, conversionRemoveCmd)
}
