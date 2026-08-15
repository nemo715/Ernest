package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/nemo715/Ernest/internal/core"
	"github.com/nemo715/Ernest/internal/knowledge"
)

// Tool names of the semantic memory tools. They are wired lazily by the
// config builder (like browser/a2a) because each agent gets tools bound
// to its own memory collection.
const (
	RememberToolName = "remember"
	RecallToolName   = "recall"
)

type rememberArgs struct {
	Text string `json:"text"`
}

type recallArgs struct {
	Query string `json:"query"`
	K     int    `json:"k,omitempty"`
}

// MemoryTools builds the remember/recall tools bound to kb. Remember
// embeds text and stores it in the agent's memory collection (scoped by
// the current run); recall retrieves the top-k most relevant memories.
func MemoryTools(kb *knowledge.KnowledgeBase) []*core.Tool {
	remember := core.MustTool(RememberToolName,
		"Store a fact, lesson or observation in the agent's long-term semantic memory. "+
			"Call this when you learn something worth remembering across future runs. "+
			"Arguments: {\"text\": \"the fact to remember\"}. Returns the stored chunk ids.",
		func(ctx context.Context, tc *core.ToolContext, args rememberArgs) (any, error) {
			if args.Text == "" {
				return nil, core.NewError(core.KindValidation, "remember: text is required")
			}
			ids, err := kb.AddText(ctx, args.Text, map[string]any{
				"agent":     tc.AgentName,
				"sessionId": tc.RunID,
				"createdAt": time.Now().UTC().Format(time.RFC3339),
			})
			if err != nil {
				return nil, err
			}
			return map[string]any{"remembered": len(ids), "ids": ids}, nil
		})

	recall := core.MustTool(RecallToolName,
		"Retrieve the top-k most relevant memories for a query. "+
			"Call this when you need prior knowledge, decisions or facts from the agent's long-term semantic memory. "+
			"Arguments: {\"query\": \"what to look for\", \"k\": 3 (optional, max 10)}. Returns matching texts with similarity scores.",
		func(ctx context.Context, tc *core.ToolContext, args recallArgs) (any, error) {
			if args.Query == "" {
				return nil, core.NewError(core.KindValidation, "recall: query is required")
			}
			k := args.K
			if k <= 0 {
				k = 3
			}
			if k > 10 {
				k = 10
			}
			results, err := kb.Query(ctx, args.Query, k)
			if err != nil {
				return nil, err
			}
			out := make([]map[string]any, 0, len(results))
			for _, r := range results {
				out = append(out, map[string]any{
					"text":      r.Text,
					"score":     fmt.Sprintf("%.4f", r.Score),
					"sessionId": r.Metadata["sessionId"],
				})
			}
			return map[string]any{"results": out}, nil
		})

	return []*core.Tool{remember, recall}
}
