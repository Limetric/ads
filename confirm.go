package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

// `ads confirm <token>` applies a staged write by token alone, so the user
// doesn't have to re-type the original command with --confirm appended. The
// pending file already stores everything needed (platform, tool, customer,
// operations), and applying exactly what was staged preserves the token/tool
// binding that the per-tool confirm path enforces (issue #6).

// ConfirmResult is a WriteResult plus the tool that staged the write, so the
// caller can see what they just applied.
type ConfirmResult struct {
	Tool string `json:"tool"`
	WriteResult
}

// runConfirm consumes the token and applies its staged mutation.
func runConfirm(ctx context.Context, c mutationApplier, token string) (ConfirmResult, error) {
	// Parity with the per-tool confirm path: guards that changed between
	// preview and confirm must still be enforced through the generic path.
	// The checks run on a peek — before the single-use token is consumed — so
	// a temporarily blocked confirm doesn't burn the token.
	peeked, err := peekMutation(token)
	if err != nil {
		return ConfirmResult{}, err
	}
	cfg := loadSafetyConfig()
	if err := checkBlockedOperation(peeked.Tool, cfg); err != nil {
		return ConfirmResult{}, toolError(peeked.Tool, err)
	}
	if err := revalidateBudgetCaps(peeked.Operations, cfg); err != nil {
		return ConfirmResult{}, toolError(peeked.Tool, err)
	}
	if err := revalidateBudgetAmounts(peeked.BudgetAmounts, cfg); err != nil {
		return ConfirmResult{}, toolError(peeked.Tool, err)
	}
	p, err := consumeMutation(token)
	if err != nil {
		return ConfirmResult{}, err
	}
	res, err := applyConsumed(ctx, c, p)
	if err != nil {
		return ConfirmResult{}, err
	}
	return ConfirmResult{Tool: p.Tool, WriteResult: res}, nil
}

// applierForToken builds the client that can apply the write staged under
// token. The pending file names the platform that staged it, so a Bing token
// is never applied through Google's API — and the client is built only for the
// platform being confirmed, so an unconfigured second platform cannot break a
// confirm for the first.
//
// A pending file with no platform (staged before the field existed, so at most
// confirmTTL old) is Google's, which is the only platform that could have
// written it.
func applierForToken(ctx context.Context, token string) (mutationApplier, string, error) {
	p, err := peekMutation(token)
	if err != nil {
		return nil, "", err
	}
	name := p.Platform
	if name == "" {
		name = googlePlatformName
	}
	plat, err := lookupPlatform(name)
	if err != nil {
		return nil, "", fmt.Errorf("confirmation %q was staged by unknown platform %q: %w", token, name, err)
	}
	if plat.NewApplier == nil {
		return nil, "", fmt.Errorf("%s has no write tools, but confirmation %q claims to be one", plat.Title, token)
	}
	applier, err := plat.NewApplier(ctx)
	if err != nil {
		return nil, "", err
	}
	return applier, name, nil
}

// --- CLI front-end ---

var confirmCmd = &cobra.Command{
	Use:   "confirm <token>",
	Short: "Apply a previously previewed write by its confirm token",
	Long:  "Apply a staged write exactly as previewed, identified by the confirm token from\nthe preview — no need to re-run the original command with --confirm.\n\nThe token remembers which platform staged it, so `ads confirm` works the same\nfor every platform. Destructive operations return a second token that must be\nconfirmed once more.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		applier, _, err := applierForToken(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		res, err := runConfirm(cmd.Context(), applier, args[0])
		if err != nil {
			return err
		}
		if err := printJSON(cmd.OutOrStdout(), res); err != nil {
			return err
		}
		// The hint goes to stderr so stdout stays valid JSON for jq pipelines.
		if !res.Applied && res.Token != "" {
			fmt.Fprintf(cmd.ErrOrStderr(), "Second confirmation required: ads confirm %s\n", res.Token)
		}
		return nil
	},
}
