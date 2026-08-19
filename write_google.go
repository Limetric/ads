package main

import (
	"context"
	"encoding/json"
	"fmt"
)

// Google's half of the write path: which endpoint a confirmed write goes to,
// and the preview helpers its tools stage through. Everything before and after
// — the confirm token, the TTL, double confirmation, the audit line — is shared
// (see safety.go and write_tool.go), so a second platform supplies only this
// file's equivalent.

// Dispatch routes a confirmed Google write to the correct REST endpoint. The
// empty value means the default googleAds:mutate path; the recommendation
// variants route to dedicated RPCs because their operations are not valid
// mutate keys.
const (
	dispatchMutate                = ""
	dispatchApplyRecommendation   = "apply_recommendation"
	dispatchDismissRecommendation = "dismiss_recommendation"
	dispatchYouTubeVideoUpload    = "youtube_video_upload"
)

// stageMutation persists a pending googleAds:mutate and returns its confirm token.
func stageMutation(tool, customerID, summary string, ops []any) (*PendingMutation, error) {
	return stageWrite(pendingWrite{
		Platform: googlePlatformName, Tool: tool, CustomerID: customerID,
		Summary: summary, Dispatch: dispatchMutate, Operations: ops,
	})
}

// stageMutationDouble is stageMutation for writes that must take a second
// confirmation regardless of the tool name — e.g. budget increases over 50%
// (issue #12).
func stageMutationDouble(tool, customerID, summary string, ops []any) (*PendingMutation, error) {
	return stageWrite(pendingWrite{
		Platform: googlePlatformName, Tool: tool, CustomerID: customerID,
		Summary: summary, Dispatch: dispatchMutate, Operations: ops, RequiresDouble: true,
	})
}

// stageDispatch persists a pending write with an explicit dispatch route. Used
// by recommendation tools that must route to dedicated RPCs on apply.
func stageDispatch(tool, customerID, summary, dispatch string, ops []any, resourceNames []string) (*PendingMutation, error) {
	return stageWrite(pendingWrite{
		Platform: googlePlatformName, Tool: tool, CustomerID: customerID,
		Summary: summary, Dispatch: dispatch, Operations: ops, ResourceNames: resourceNames,
	})
}

// previewMutate stages a default googleAds:mutate write and returns its preview.
func previewMutate(tool, customerID, summary string, ops []any) (WriteResult, error) {
	p, err := stageMutation(tool, customerID, summary, ops)
	if err != nil {
		return WriteResult{}, err
	}
	return previewResult(p), nil
}

// previewMutateDouble is previewMutate for writes that must take a second
// confirmation (e.g. budget increases over 50% — issue #12).
func previewMutateDouble(tool, customerID, summary string, ops []any) (WriteResult, error) {
	p, err := stageMutationDouble(tool, customerID, summary, ops)
	if err != nil {
		return WriteResult{}, err
	}
	return previewResult(p), nil
}

// platformName makes *Client a mutationApplier: Google's namespace, which the
// confirm path checks a staged write against before applying it.
func (c *Client) platformName() string { return googlePlatformName }

// applyMutation makes *Client a mutationApplier: it executes a consumed pending
// write via the endpoint selected by its Dispatch — the dedicated
// recommendation RPCs, the resumable upload, or the default mutate path.
func (c *Client) applyMutation(ctx context.Context, p *PendingMutation) (*applyOutcome, error) {
	switch p.Dispatch {
	case dispatchApplyRecommendation:
		response, err := c.ApplyRecommendations(ctx, p.CustomerID, p.ResourceNames)
		if err != nil {
			return nil, err
		}
		if err := partialFailureError(response.PartialErrors); err != nil {
			return nil, err
		}
		return &applyOutcome{Results: response.Results}, nil
	case dispatchDismissRecommendation:
		response, err := c.DismissRecommendations(ctx, p.CustomerID, p.ResourceNames)
		if err != nil {
			return nil, err
		}
		if err := partialFailureError(response.PartialErrors); err != nil {
			return nil, err
		}
		return &applyOutcome{Results: response.Results}, nil
	case dispatchYouTubeVideoUpload:
		if len(p.Operations) != 1 {
			return nil, fmt.Errorf("YouTube video upload confirmation has %d operations; expected 1", len(p.Operations))
		}
		operation, ok := p.Operations[0].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("YouTube video upload confirmation is corrupt")
		}
		filePath, _ := operation["file_path"].(string)
		title, _ := operation["title"].(string)
		description, _ := operation["description"].(string)
		response, err := c.UploadYouTubeVideo(ctx, p.CustomerID, filePath, title, description)
		if err != nil {
			return nil, err
		}
		result, err := json.Marshal(map[string]any{"resourceName": response.ResourceName})
		if err != nil {
			return nil, err
		}
		return &applyOutcome{Results: []json.RawMessage{result}}, nil
	default:
		response, err := c.Mutate(ctx, p.CustomerID, p.Operations)
		if err != nil {
			return nil, err
		}
		if err := partialFailureError(response.PartialErrors); err != nil {
			return nil, err
		}
		return &applyOutcome{Results: response.operationResults()}, nil
	}
}

// partialFailureError turns a googleAds:mutate partialFailureError payload into
// a Go error, so a mutation that only partly applied fails the tool call rather
// than reporting success (issue #7).
func partialFailureError(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "{}" {
		return nil
	}
	var status struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Details json.RawMessage `json:"details"`
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		return fmt.Errorf("decode Google Ads partial failure: %w", err)
	}
	if status.Code == 0 && status.Message == "" && len(status.Details) == 0 {
		return nil
	}
	if status.Message == "" {
		status.Message = string(raw)
	}
	return fmt.Errorf("google ads mutation partially failed (code %d): %s", status.Code, status.Message)
}
