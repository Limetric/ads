package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// UploadYouTubeVideoArgs uploads a local MP4 to the Google-managed, unlisted
// YouTube channel associated with the Ads account.
type UploadYouTubeVideoArgs struct {
	CustomerID  string `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID that will own the upload; omit to use the configured default customer"`
	VideoFile   string `json:"video_file" jsonschema:"absolute path to the local MP4 file"`
	Title       string `json:"title" jsonschema:"the YouTube video title"`
	Description string `json:"description,omitempty" jsonschema:"the YouTube video description"`
	Confirm     string `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

func runUploadYouTubeVideo(ctx context.Context, c *Client, args UploadYouTubeVideoArgs) (WriteResult, error) {
	const tool = "upload_youtube_video"
	if err := checkBlockedOperation(tool, loadSafetyConfig()); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if args.VideoFile == "" {
		return WriteResult{}, fmt.Errorf("video_file is required")
	}
	if strings.ToLower(filepath.Ext(args.VideoFile)) != ".mp4" {
		return WriteResult{}, fmt.Errorf("video_file must be an MP4")
	}
	info, err := os.Stat(args.VideoFile)
	if err != nil {
		return WriteResult{}, fmt.Errorf("stat video_file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return WriteResult{}, fmt.Errorf("video_file must be a non-empty regular file")
	}
	if args.Title == "" {
		return WriteResult{}, fmt.Errorf("title is required")
	}
	if args.Confirm != "" {
		return applyConfirmed(ctx, c, tool, args.Confirm)
	}
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return WriteResult{}, err
	}
	operation := map[string]any{
		"file_path":   args.VideoFile,
		"title":       args.Title,
		"description": args.Description,
	}
	summary := fmt.Sprintf("Upload %q (%d bytes) to the Google-managed YouTube channel as UNLISTED", args.Title, info.Size())
	pending, err := stageDispatch(tool, cid, summary, dispatchYouTubeVideoUpload, []any{operation}, nil)
	if err != nil {
		return WriteResult{}, err
	}
	return previewResult(pending), nil
}

var uploadYouTubeVideoArgs UploadYouTubeVideoArgs

var assetUploadVideoCmd = &cobra.Command{
	Use:   "upload-video",
	Short: "Upload an MP4 to a Google-managed YouTube channel (previews first; --confirm to apply)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		result, err := runUploadYouTubeVideo(cmd.Context(), client, uploadYouTubeVideoArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), result)
	},
}

func init() {
	assetUploadVideoCmd.Flags().StringVar(&uploadYouTubeVideoArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	assetUploadVideoCmd.Flags().StringVar(&uploadYouTubeVideoArgs.VideoFile, "video-file", "", "path to an MP4 file (required)")
	assetUploadVideoCmd.Flags().StringVar(&uploadYouTubeVideoArgs.Title, "title", "", "YouTube video title (required)")
	assetUploadVideoCmd.Flags().StringVar(&uploadYouTubeVideoArgs.Description, "description", "", "YouTube video description")
	assetUploadVideoCmd.Flags().StringVar(&uploadYouTubeVideoArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = assetUploadVideoCmd.MarkFlagRequired("video-file")
	_ = assetUploadVideoCmd.MarkFlagRequired("title")

	assetCmd.AddCommand(assetUploadVideoCmd)
}
