package mcp

import (
	"context"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPElicitationOnWriteConflict(t *testing.T) {
	home := fixtureHome(t, false)
	ctx := context.Background()
	ct, st := mcpsdk.NewInMemoryTransports()

	srv, err := NewServer(Options{Root: home, Agent: "test"})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })

	elicited := false
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "elicit-client", Version: "1.0"}, &mcpsdk.ClientOptions{
		ElicitationHandler: func(_ context.Context, _ *mcpsdk.ElicitRequest) (*mcpsdk.ElicitResult, error) {
			elicited = true
			return &mcpsdk.ElicitResult{
				Action:  "accept",
				Content: map[string]any{"resolution": "supersede"},
			}, nil
		},
	})
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	// First write: unique record.
	r1, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "memory_add",
		Arguments: map[string]any{
			"layer": "lesson",
			"scope": "global",
			"text":  "always validate jwt issuer before checking claims\n**NOT.** never skip issuer check",
		},
	})
	if err != nil || r1.IsError {
		t.Fatalf("first add failed: %v, %+v", err, r1)
	}

	// Second write: near-duplicate (>=0.88 Jaccard) triggers InputRequests ("conflict") ->
	// go-sdk invokes ElicitationHandler -> auto-retries with InputResponses -> supersedes.
	r2, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "memory_add",
		Arguments: map[string]any{
			"layer": "lesson",
			"scope": "global",
			"text":  "always validate jwt issuer before checking claims!\n**NOT.** never skip issuer check",
		},
	})
	if err != nil || r2.IsError || len(r2.Content) == 0 {
		t.Fatalf("second add via elicitation failed: err=%v res=%+v", err, r2)
	}
	if !elicited {
		t.Fatalf("expected ElicitationHandler to be invoked on write conflict")
	}
	txt := r2.Content[0].(*mcpsdk.TextContent).Text
	if !strings.Contains(txt, "superseded=") {
		t.Fatalf("expected superseded= in response, got: %q", txt)
	}
}
