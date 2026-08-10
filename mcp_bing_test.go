package main

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// bingMCPSession wires an in-memory MCP client to a server with Bing's tools
// registered, through the same registrar the real server uses — so the names
// carry the `bing_` prefix exactly as they would in production.
func bingMCPSession(t *testing.T, client *BingClient) *mcp.ClientSession {
	t.Helper()
	serverT, clientT := mcp.NewInMemoryTransports()
	server := mcp.NewServer(&mcp.Implementation{Name: "ads", Version: "test"}, nil)
	addBingTools(&toolRegistrar{server: server, prefix: bingPlatform.Name + "_"}, client)

	ctx := context.Background()
	ss, err := server.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	mc := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := mc.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() {
		_ = cs.Close()
		_ = ss.Close()
	})
	return cs
}

func TestMCP_BingRegistrationIntegrity(t *testing.T) {
	cs := bingMCPSession(t, &BingClient{cfg: &BingConfig{}})
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range res.Tools {
		if names[tool.Name] {
			t.Errorf("duplicate MCP tool registered: %q", tool.Name)
		}
		names[tool.Name] = true
		if tool.InputSchema == nil {
			t.Errorf("tool %q has no input schema", tool.Name)
		}
		if !strings.HasPrefix(tool.Name, bingPlatformName+"_") {
			t.Errorf("MCP tool %q is not namespaced", tool.Name)
		}
	}
	for _, want := range []string{
		"bing_list_accounts", "bing_account_info", "bing_campaigns", "bing_ad_groups", "bing_keywords",
		"bing_campaign_performance", "bing_keyword_performance", "bing_ad_performance",
		"bing_report_fetch", "bing_set_campaign_budget",
	} {
		if !names[want] {
			t.Errorf("MCP tool %q is not registered", want)
		}
	}
	if len(names) != 10 {
		t.Errorf("expected 10 registered Bing MCP tools, got %d (update this count when adding/removing a tool)", len(names))
	}
}

func TestMCP_BingCallCampaignsTool(t *testing.T) {
	srv := bingJSONServer(t, map[string]string{
		"/Campaigns/QueryByAccountId": `{"Campaigns":[{"Id":"1","Name":"Brand","DailyBudget":25.5}]}`,
	})
	defer srv.Close()
	cs := bingMCPSession(t, newTestBingClient(t, srv))

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "bing_campaigns",
		Arguments: map[string]any{"account_id": "123456"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("bing_campaigns returned an error result: %+v", res.Content)
	}
	var text strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	if !strings.Contains(text.String(), "Brand") {
		t.Errorf("tool output: %s", text.String())
	}
	if res.StructuredContent == nil {
		t.Error("expected structured content to be populated")
	}
}

func TestRegisterTools_SkipsAPlatformThatCannotAuthenticate(t *testing.T) {
	useTempState(t)
	clearAdsEnv(t)
	clearBingEnv(t)

	warnings := captureWarnings(t)

	// Google is pointed at a loopback URL (so it needs no credentials); Bing has
	// none at all. A user in exactly this position — signed in to one network —
	// must still get a working server.
	t.Setenv("GOOGLE_ADS_API_BASE_URL", "http://127.0.0.1:1")

	server := mcp.NewServer(&mcp.Implementation{Name: "ads", Version: "test"}, nil)
	if err := registerTools(context.Background(), server); err != nil {
		t.Fatalf("one unconfigured platform must not take down the server: %v", err)
	}
	if !strings.Contains(warnings.String(), "Microsoft Advertising") {
		t.Errorf("the skipped platform should be named on stderr: %q", warnings.String())
	}
	if !strings.Contains(warnings.String(), "ads doctor bing") {
		t.Errorf("the warning should say how to fix it: %q", warnings.String())
	}
}

func TestRegisterTools_FailsWhenNothingCanBeServed(t *testing.T) {
	useTempState(t)
	clearAdsEnv(t)
	clearBingEnv(t)
	captureWarnings(t)

	// Neither platform has credentials. An MCP host cannot surface an empty
	// tool list as a setup problem, so this case has to be loud.
	server := mcp.NewServer(&mcp.Implementation{Name: "ads", Version: "test"}, nil)
	err := registerTools(context.Background(), server)
	if err == nil {
		t.Fatal("a server with no usable platform must fail to start")
	}
	if !strings.Contains(err.Error(), "ads doctor") {
		t.Errorf("error = %v", err)
	}
}
