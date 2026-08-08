package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// This file pauses, enables, and removes campaigns, ad groups, ads, or keywords.
// All writes preview first; remove is destructive and is flagged as such in the
// preview.

var validEntityTypes = []string{"campaign", "ad_group", "ad", "keyword"}

// entityResourceAndOp maps an entity type to its REST resource path and the
// mutate operation key that targets it. Campaigns and ad groups take a plain
// numeric ID; ads and keywords live under composite adGroupId~entityId
// resources — a bare ID would preview fine and fail only at confirm with an
// invalid-resource-name error (issue #14).
func entityResourceAndOp(cid, entityType, entityID string) (resource, opKey string, err error) {
	switch entityType {
	case "campaign", "ad_group":
		if _, err := numericID("entity_id", entityID); err != nil {
			return "", "", err
		}
	case "ad", "keyword":
		if err := validateCompositeID(entityType, entityID); err != nil {
			return "", "", err
		}
	}
	switch entityType {
	case "campaign":
		return fmt.Sprintf("customers/%s/campaigns/%s", cid, entityID), "campaignOperation", nil
	case "ad_group":
		return fmt.Sprintf("customers/%s/adGroups/%s", cid, entityID), "adGroupOperation", nil
	case "ad":
		return fmt.Sprintf("customers/%s/adGroupAds/%s", cid, entityID), "adGroupAdOperation", nil
	case "keyword":
		return fmt.Sprintf("customers/%s/adGroupCriteria/%s", cid, entityID), "adGroupCriterionOperation", nil
	default:
		return "", "", fmt.Errorf("invalid entity type %q: must be one of: %s", entityType, strings.Join(validEntityTypes, ", "))
	}
}

// validateCompositeID checks the adGroupId~entityId shape ads and keywords use.
func validateCompositeID(entityType, id string) error {
	parts := strings.Split(id, "~")
	if len(parts) != 2 {
		return fmt.Errorf("entity_id for a %s must be the composite adGroupId~%sId (e.g. 111~222), got %q", entityType, entityType, id)
	}
	for _, p := range parts {
		if _, err := numericID("entity_id", p); err != nil {
			return fmt.Errorf("entity_id for a %s must be the composite adGroupId~%sId with numeric parts, got %q", entityType, entityType, id)
		}
	}
	return nil
}

// EntityActionArgs pauses, enables, or removes a single entity.
type EntityActionArgs struct {
	CustomerID string `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID that owns the entity; omit to use the configured default customer"`
	EntityType string `json:"entity_type" jsonschema:"one of: campaign, ad_group, ad, keyword"`
	EntityID   string `json:"entity_id" jsonschema:"the entity ID; for an ad or keyword this is the composite adGroupId~entityId (e.g. 111~222)"`
	Confirm    string `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

func runPauseEntity(ctx context.Context, c *Client, args EntityActionArgs) (WriteResult, error) {
	return entityStatusChange(ctx, c, args, "pause_entity", string(AdStatusPaused))
}

func runEnableEntity(ctx context.Context, c *Client, args EntityActionArgs) (WriteResult, error) {
	return entityStatusChange(ctx, c, args, "enable_entity", string(AdStatusEnabled))
}

// entityStatusChange stages or applies a status update on an entity.
func entityStatusChange(ctx context.Context, c *Client, args EntityActionArgs, tool, status string) (WriteResult, error) {
	if err := checkBlockedOperation(tool, loadSafetyConfig()); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if args.EntityID == "" {
		return WriteResult{}, fmt.Errorf("entity_id is required")
	}
	if args.Confirm != "" {
		return applyConfirmed(ctx, c, tool, args.Confirm)
	}
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return WriteResult{}, err
	}
	resource, opKey, err := entityResourceAndOp(cid, args.EntityType, args.EntityID)
	if err != nil {
		return WriteResult{}, err
	}
	op := map[string]any{
		opKey: map[string]any{
			"update":     map[string]any{"resourceName": resource, "status": status},
			"updateMask": "status",
		},
	}
	summary := fmt.Sprintf("Set %s %s status to %s", args.EntityType, args.EntityID, status)
	return previewMutate(tool, cid, summary, []any{op})
}

func runRemoveEntity(ctx context.Context, c *Client, args EntityActionArgs) (WriteResult, error) {
	const tool = "remove_entity"
	if err := checkBlockedOperation(tool, loadSafetyConfig()); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if args.EntityID == "" {
		return WriteResult{}, fmt.Errorf("entity_id is required")
	}
	if args.Confirm != "" {
		return applyConfirmed(ctx, c, tool, args.Confirm)
	}
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return WriteResult{}, err
	}
	resource, opKey, err := entityResourceAndOp(cid, args.EntityType, args.EntityID)
	if err != nil {
		return WriteResult{}, err
	}
	op := map[string]any{opKey: map[string]any{"remove": resource}}
	summary := fmt.Sprintf("REMOVE %s %s — destructive and cannot be undone", args.EntityType, args.EntityID)
	return previewMutate(tool, cid, summary, []any{op})
}

// --- CLI front-end ---

var (
	pauseArgs  EntityActionArgs
	enableArgs EntityActionArgs
	removeArgs EntityActionArgs
)

func entityCmd(use, short string, args *EntityActionArgs, run func(context.Context, *Client, EntityActionArgs) (WriteResult, error)) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := newGoogleClient(cmd.Context())
			if err != nil {
				return err
			}
			res, err := run(cmd.Context(), client, *args)
			if err != nil {
				return err
			}
			return printJSON(cmd.OutOrStdout(), res)
		},
	}
	cmd.Flags().StringVar(&args.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	cmd.Flags().StringVar(&args.EntityType, "type", "", "entity type: campaign, ad_group, ad, or keyword (required)")
	cmd.Flags().StringVar(&args.EntityID, "id", "", "entity ID (required)")
	cmd.Flags().StringVar(&args.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = cmd.MarkFlagRequired("type")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}

var (
	pauseCmd  = entityCmd("pause", "Pause a campaign, ad group, ad, or keyword", &pauseArgs, runPauseEntity)
	enableCmd = entityCmd("enable", "Enable a campaign, ad group, ad, or keyword", &enableArgs, runEnableEntity)
	removeCmd = entityCmd("remove", "Remove a campaign, ad group, ad, or keyword (destructive)", &removeArgs, runRemoveEntity)
)
