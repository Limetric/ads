package main

import (
	"context"
	"encoding/json"
	"fmt"
)

// Bing's half of the write path. Staging, the confirm token, its TTL, double
// confirmation, and the audit line are all shared (safety.go, write_tool.go);
// this file supplies only the endpoint a confirmed write goes to.

// Dispatch routes a confirmed Bing write to the correct operation. Microsoft
// has no single mutate endpoint — every entity type has its own — so unlike
// Google there is no meaningful default, and each write tool names its route.
const dispatchBingUpdateCampaign = "bing_update_campaign"

// previewBingWrite stages a Bing write and returns its preview.
func previewBingWrite(w pendingWrite) (WriteResult, error) {
	w.Platform = bingPlatformName
	p, err := stageWrite(w)
	if err != nil {
		return WriteResult{}, err
	}
	return previewResult(p), nil
}

// platformName makes *BingClient a mutationApplier: Bing's namespace, which the
// confirm path checks a staged write against before applying it.
func (c *BingClient) platformName() string { return bingPlatformName }

// applyMutation makes *BingClient a mutationApplier: it executes a consumed
// pending write against the operation its Dispatch names.
//
// Partial success is checked on every route. Microsoft applies a batch
// item-by-item and returns entries only for the ones that failed, so a caller
// that ignores PartialErrors reports success for a write that did not happen.
func (c *BingClient) applyMutation(ctx context.Context, p *PendingMutation) (*applyOutcome, error) {
	switch p.Dispatch {
	case dispatchBingUpdateCampaign:
		return c.applyCampaignUpdate(ctx, p)
	default:
		return nil, fmt.Errorf("unknown Microsoft Advertising write route %q — the staged operation cannot be applied; re-run the original command for a fresh preview", p.Dispatch)
	}
}

// applyCampaignUpdate applies a staged UpdateCampaigns call.
func (c *BingClient) applyCampaignUpdate(ctx context.Context, p *PendingMutation) (*applyOutcome, error) {
	campaigns, err := bingCampaignOperations(p.Operations)
	if err != nil {
		return nil, err
	}
	response, err := c.UpdateCampaigns(ctx, p.CustomerID, campaigns)
	if err != nil {
		return nil, err
	}
	if err := bingPartialErrors(response.PartialErrors, len(campaigns)); err != nil {
		return nil, err
	}
	results := make([]json.RawMessage, 0, len(campaigns))
	for _, campaign := range campaigns {
		encoded, err := json.Marshal(campaign)
		if err != nil {
			return nil, err
		}
		results = append(results, encoded)
	}
	return &applyOutcome{Results: results}, nil
}

// bingCampaignOperations reads the staged campaign payloads back.
//
// A pending file is JSON that has been to disk and back, so the operations
// arrive as map[string]any regardless of what staged them. Anything else means
// the file was tampered with or written by a different version, and applying a
// half-understood payload to a live account is not the way to find out which.
func bingCampaignOperations(ops []any) ([]any, error) {
	if len(ops) == 0 {
		return nil, fmt.Errorf("campaign update confirmation has no operations")
	}
	campaigns := make([]any, 0, len(ops))
	for i, op := range ops {
		m, ok := op.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("campaign update confirmation is corrupt: operation %d is not an object", i)
		}
		if _, ok := m["Id"]; !ok {
			return nil, fmt.Errorf("campaign update confirmation is corrupt: operation %d has no campaign Id", i)
		}
		campaigns = append(campaigns, m)
	}
	return campaigns, nil
}
