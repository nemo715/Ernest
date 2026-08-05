package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"ernest/internal/agent"
	"ernest/internal/core"
	"ernest/internal/llm"
)

func testMCPServer() *Server {
	p := llm.NewMock(llm.MockConfig{
		Script: []llm.MockTurn{
			{Content: "Hello from assistant", FinishReason: "stop"},
		},
	})
	a := agent.New("assistant", p)
	a.Description = "General assistant"
	p2 := llm.NewMock(llm.MockConfig{
		Script: []llm.MockTurn{
			{ToolCalls: []core.ToolCall{{ID: "c1", Name: "calculator", Arguments: []byte(`{"expression":"2+2"}`)}}, FinishReason: "tool_calls"},
			{Content: "4", FinishReason: "stop"},
		},
	})
	math := agent.New("math", p2)
	math.Tools = []*core.Tool{core.Calculator}
	return NewServer([]*agent.Agent{a, math}, ServerOptions{Name: "test", Version: "9.9.9"})
}

// serve runs the server against a request payload and returns the
// response lines.
func serve(t *testing.T, srv *Server, payload string) []string {
	t.Helper()
	var out bytes.Buffer
	err := srv.ServeStdio(context.Background(), strings.NewReader(payload), &out)
	if err != nil {
		t.Fatalf("ServeStdio: %v", err)
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func TestMCPServerInitialize(t *testing.T) {
	srv := testMCPServer()
	lines := serve(t, srv, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"x","version":"1"}}}`+"\n")
	if len(lines) != 1 {
		t.Fatalf("lines = %v", lines)
	}
	var res struct {
		ID     int `json:"id"`
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &res); err != nil {
		t.Fatal(err)
	}
	if res.ID != 1 || res.Result.ProtocolVersion != ProtocolVersion {
		t.Fatalf("res = %+v", res)
	}
	if res.Result.ServerInfo.Name != "test" || res.Result.ServerInfo.Version != "9.9.9" {
		t.Fatalf("serverInfo = %+v", res.Result.ServerInfo)
	}
}

func TestMCPServerToolList(t *testing.T) {
	srv := testMCPServer()
	lines := serve(t, srv, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`+"\n")
	var res struct {
		Result struct {
			Tools []struct {
				Name        string         `json:"name"`
				Description string         `json:"description"`
				InputSchema map[string]any `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Result.Tools) != 2 {
		t.Fatalf("tools = %+v", res.Result.Tools)
	}
	if res.Result.Tools[0].Name != "assistant" || res.Result.Tools[1].Name != "math" {
		t.Fatalf("names = %s, %s", res.Result.Tools[0].Name, res.Result.Tools[1].Name)
	}
	if _, ok := res.Result.Tools[0].InputSchema["properties"]; !ok {
		t.Fatalf("inputSchema = %v", res.Result.Tools[0].InputSchema)
	}
}

func TestMCPServerToolCall(t *testing.T) {
	srv := testMCPServer()
	lines := serve(t, srv, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"assistant","arguments":{"input":"hi"}}}`+"\n")
	var res struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &res); err != nil {
		t.Fatal(err)
	}
	if res.Result.IsError {
		t.Fatalf("unexpected error result: %+v", res.Result)
	}
	if len(res.Result.Content) != 1 || res.Result.Content[0].Text != "Hello from assistant" {
		t.Fatalf("content = %+v", res.Result.Content)
	}
}

func TestMCPServerToolCallAgentWithTools(t *testing.T) {
	// The math agent runs a calculator tool then answers.
	srv := testMCPServer()
	lines := serve(t, srv, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"math","arguments":{"input":"2+2?"}}}`+"\n")
	var res struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &res); err != nil {
		t.Fatal(err)
	}
	if res.Result.IsError {
		t.Fatalf("unexpected error: %+v", res.Result)
	}
	if res.Result.Content[0].Text != "4" {
		t.Fatalf("text = %q", res.Result.Content[0].Text)
	}
}

func TestMCPServerErrors(t *testing.T) {
	srv := testMCPServer()
	// Unknown tool → JSON-RPC error with the agent list hint.
	lines := serve(t, srv, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"nope","arguments":{"input":"x"}}}`+"\n")
	var res struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &res); err != nil {
		t.Fatal(err)
	}
	if res.Error.Code != -32602 || !strings.Contains(res.Error.Message, "assistant, math") {
		t.Fatalf("error = %+v", res.Error)
	}

	// Missing input → invalid params.
	lines = serve(t, srv, `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"assistant","arguments":{}}}`+"\n")
	if err := json.Unmarshal([]byte(lines[0]), &res); err != nil {
		t.Fatal(err)
	}
	if res.Error.Code != -32602 {
		t.Fatalf("error = %+v", res.Error)
	}

	// Unknown method.
	lines = serve(t, srv, `{"jsonrpc":"2.0","id":7,"method":"bogus"}`+"\n")
	if err := json.Unmarshal([]byte(lines[0]), &res); err != nil {
		t.Fatal(err)
	}
	if res.Error.Code != -32601 {
		t.Fatalf("error = %+v", res.Error)
	}
}

func TestMCPServerNotificationsAndPing(t *testing.T) {
	srv := testMCPServer()
	payload := strings.Join([]string{
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","id":8,"method":"ping"}`,
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`,
		"",
	}, "\n")
	lines := serve(t, srv, payload)
	if len(lines) != 1 {
		t.Fatalf("lines = %v", lines)
	}
	var res struct {
		ID     int            `json:"id"`
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &res); err != nil {
		t.Fatal(err)
	}
	if res.ID != 8 || res.Result == nil {
		t.Fatalf("res = %+v", res)
	}
}

func TestMCPServerContentLengthFraming(t *testing.T) {
	srv := testMCPServer()
	req := `{"jsonrpc":"2.0","id":9,"method":"tools/list"}`
	payload := "Content-Length: " + strconv.Itoa(len(req)) + "\r\n\r\n" + req + "\n"
	lines := serve(t, srv, payload)
	if len(lines) != 1 {
		t.Fatalf("lines = %v", lines)
	}
	if !strings.Contains(lines[0], `"tools"`) {
		t.Fatalf("line = %s", lines[0])
	}
}

func TestMCPServerStringID(t *testing.T) {
	srv := testMCPServer()
	lines := serve(t, srv, `{"jsonrpc":"2.0","id":"abc","method":"ping"}`+"\n")
	var res struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &res); err != nil {
		t.Fatal(err)
	}
	if res.ID != "abc" {
		t.Fatalf("id = %q", res.ID)
	}
}
