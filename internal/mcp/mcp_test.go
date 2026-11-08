package mcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// newSession starts the skeleton server plus a test client over in-memory
// transports and performs the initialize handshake (Client.Connect
// negotiates protocol version and capabilities). Returns the client session.
func newSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()

	serverSession, err := NewServer().Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { serverSession.Close() })

	cs, err := mcp.NewClient(&mcp.Implementation{Name: "cli-test-client"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect/initialize: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// TestInitializeHandshake: connect + initialize against the skeleton
// server; the server must present the my-memory identity.
func TestInitializeHandshake(t *testing.T) {
	cs := newSession(t)

	ir := cs.InitializeResult()
	if ir == nil {
		t.Fatal("initialize result is nil — handshake did not complete")
	}
	if ir.ServerInfo.Name != ServerName {
		t.Errorf("server name = %q, want %q", ir.ServerInfo.Name, ServerName)
	}
	if ir.ServerInfo.Version != Version {
		t.Errorf("server version = %q, want %q", ir.ServerInfo.Version, Version)
	}
}

// TestToolsListNineFrozenTools: tools/list must return exactly the 9
// frozen tools (OQ5) with the baseline names.
func TestToolsListNineFrozenTools(t *testing.T) {
	res, err := newSession(t).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if len(res.Tools) != 9 {
		names := make([]string, len(res.Tools))
		for i, tool := range res.Tools {
			names[i] = tool.Name
		}
		t.Fatalf("tools/list returned %d tools (%v), want 9", len(res.Tools), names)
	}
	got := make([]string, len(res.Tools))
	for i, tool := range res.Tools {
		got[i] = tool.Name
	}
	// The registry order is the baseline's MCP_TOOLS order; tools/list
	// itself sorts alphabetically, so compare the frozen set as sets.
	slices.Sort(got)
	want := slices.Clone(ToolNames)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("tool names = %v, want the frozen set %v", got, want)
	}
}

// TestToolCallStub: a tools/call round trip hits the stub handler and
// returns its text result naming the tool.
func TestToolCallStub(t *testing.T) {
	res, err := newSession(t).CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "memory_context",
		Arguments: map[string]any{"query": "งานที่กำลังทำ"},
	})
	if err != nil {
		t.Fatalf("tools/call memory_context: %v", err)
	}
	if res.IsError {
		t.Fatal("memory_context stub returned a tool error")
	}
	if len(res.Content) == 0 {
		t.Fatal("memory_context stub returned no content")
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content[0] is %T, want *mcp.TextContent", res.Content[0])
	}
	if !strings.HasPrefix(text.Text, "memory_context:") {
		t.Errorf("stub text = %q, want prefix %q", text.Text, "memory_context:")
	}
}

// TestRequiredArgsEnforced: the inferred schema keeps the baseline's
// required fields — memory_search without a query must be rejected.
func TestRequiredArgsEnforced(t *testing.T) {
	// Schema violations come back as a tool-level error result (the SDK
	// reports validation failures in content, not as a transport error).
	res, err := newSession(t).CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "memory_search",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("tools/call memory_search: %v", err)
	}
	if !res.IsError {
		t.Error("memory_search without required query was accepted, want schema rejection")
	}
}
