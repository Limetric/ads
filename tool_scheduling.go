package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// This file sets campaign ad schedules (day-of-week + time windows). It is a
// write that previews first.

var validScheduleDays = map[string]bool{
	"MONDAY": true, "TUESDAY": true, "WEDNESDAY": true, "THURSDAY": true,
	"FRIDAY": true, "SATURDAY": true, "SUNDAY": true,
}

// ScheduleEntry is one ad-schedule window. Valid minutes are 0, 15, 30, 45;
// valid hours are 0-24.
type ScheduleEntry struct {
	DayOfWeek   string `json:"day_of_week" jsonschema:"day of week, e.g. MONDAY"`
	StartHour   int    `json:"start_hour" jsonschema:"start hour 0-24"`
	StartMinute int    `json:"start_minute" jsonschema:"start minute: 0, 15, 30, or 45"`
	EndHour     int    `json:"end_hour" jsonschema:"end hour 0-24"`
	EndMinute   int    `json:"end_minute" jsonschema:"end minute: 0, 15, 30, or 45"`
}

// SetScheduleArgs sets one or more ad schedules on a campaign.
type SetScheduleArgs struct {
	CustomerID string          `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID that owns the campaign; omit to use the configured default customer"`
	CampaignID string          `json:"campaign_id" jsonschema:"the campaign ID to schedule"`
	Schedules  []ScheduleEntry `json:"schedules" jsonschema:"the ad-schedule windows to set"`
	Confirm    string          `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

// minuteEnum converts a minute value to the Google Ads MinuteOfHour enum string.
func minuteEnum(minute int) string {
	switch minute {
	case 15:
		return "FIFTEEN"
	case 30:
		return "THIRTY"
	case 45:
		return "FORTY_FIVE"
	default:
		return "ZERO"
	}
}

func validateSchedule(s ScheduleEntry) error {
	if !validScheduleDays[s.DayOfWeek] {
		return fmt.Errorf("invalid day %q: must be MONDAY..SUNDAY (uppercase)", s.DayOfWeek)
	}
	if s.StartHour > 24 || s.EndHour > 24 || s.StartHour < 0 || s.EndHour < 0 {
		return fmt.Errorf("hours must be 0-24, got start=%d end=%d", s.StartHour, s.EndHour)
	}
	valid := map[int]bool{0: true, 15: true, 30: true, 45: true}
	if !valid[s.StartMinute] || !valid[s.EndMinute] {
		return fmt.Errorf("minutes must be 0, 15, 30, or 45, got start=%d end=%d", s.StartMinute, s.EndMinute)
	}
	// The window must end after it starts (issue #14).
	if s.EndHour*60+s.EndMinute <= s.StartHour*60+s.StartMinute {
		return fmt.Errorf("schedule window must end after it starts, got %02d:%02d-%02d:%02d", s.StartHour, s.StartMinute, s.EndHour, s.EndMinute)
	}
	return nil
}

func runSetCampaignSchedule(ctx context.Context, c *Client, args SetScheduleArgs) (WriteResult, error) {
	const tool = "set_campaign_schedule"
	if err := checkBlockedOperation(tool, loadSafetyConfig()); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if len(args.Schedules) == 0 {
		return WriteResult{}, fmt.Errorf("at least one schedule entry is required")
	}
	for _, s := range args.Schedules {
		if err := validateSchedule(s); err != nil {
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
	if _, err := numericID("campaign_id", args.CampaignID); err != nil {
		return WriteResult{}, err
	}
	campaignResource := fmt.Sprintf("customers/%s/campaigns/%s", cid, args.CampaignID)
	ops := make([]any, len(args.Schedules))
	for i, s := range args.Schedules {
		ops[i] = map[string]any{
			"campaignCriterionOperation": map[string]any{
				"create": map[string]any{
					"campaign": campaignResource,
					"adSchedule": map[string]any{
						"dayOfWeek":   s.DayOfWeek,
						"startHour":   s.StartHour,
						"startMinute": minuteEnum(s.StartMinute),
						"endHour":     s.EndHour,
						"endMinute":   minuteEnum(s.EndMinute),
					},
				},
			},
		}
	}
	summary := fmt.Sprintf("Add %d ad-schedule window(s) to campaign %s (additive: existing windows are kept)", len(args.Schedules), args.CampaignID)
	return previewMutate(tool, cid, summary, ops)
}

// --- CLI front-end ---

var (
	scheduleArgs    SetScheduleArgs
	scheduleStrings []string
)

// parseScheduleFlag parses "DAY,startHour,startMinute,endHour,endMinute".
func parseScheduleFlag(v string) (ScheduleEntry, error) {
	parts := strings.Split(v, ",")
	if len(parts) != 5 {
		return ScheduleEntry{}, fmt.Errorf("schedule %q must be DAY,startHour,startMinute,endHour,endMinute", v)
	}
	nums := make([]int, 4)
	for i, p := range parts[1:] {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return ScheduleEntry{}, fmt.Errorf("schedule %q: %q is not a number", v, p)
		}
		nums[i] = n
	}
	return ScheduleEntry{
		DayOfWeek:   strings.ToUpper(strings.TrimSpace(parts[0])),
		StartHour:   nums[0],
		StartMinute: nums[1],
		EndHour:     nums[2],
		EndMinute:   nums[3],
	}, nil
}

var scheduleCmd = &cobra.Command{
	Use:   "schedule",
	Short: "Set campaign ad schedules (previews first; --confirm to apply)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		for _, s := range scheduleStrings {
			entry, err := parseScheduleFlag(s)
			if err != nil {
				return err
			}
			scheduleArgs.Schedules = append(scheduleArgs.Schedules, entry)
		}
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runSetCampaignSchedule(cmd.Context(), client, scheduleArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

func init() {
	scheduleCmd.Flags().StringVar(&scheduleArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	scheduleCmd.Flags().StringVar(&scheduleArgs.CampaignID, "campaign-id", "", "campaign ID (required)")
	scheduleCmd.Flags().StringArrayVar(&scheduleStrings, "schedule", nil, "schedule window DAY,startHour,startMinute,endHour,endMinute (repeatable)")
	scheduleCmd.Flags().StringVar(&scheduleArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = scheduleCmd.MarkFlagRequired("campaign-id")
}
