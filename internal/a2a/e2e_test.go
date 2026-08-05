package a2a_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ernest/internal/a2a"
	"ernest/internal/agent"
	"ernest/internal/llm"
	"ernest/internal/server"
)

func newServerWithAgent(t *testing.T) *server.Server {
	t.Helper()
	p := llm.NewMock(llm.MockConfig{
		Script: []llm.MockTurn{
			{Content: "Hello from the far side", FinishReason: "stop"},
		},
	})
	a := agent.New("far", p)
	srv, err := server.New(server.Options{Agents: []*agent.Agent{a}})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func TestA2AWellKnownAndCard(t *testing.T) {
	srv := newServerWithAgent(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	res, err := http.Get(ts.URL + "/.well-known/agent.json")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var doc struct {
		Agents []a2a.AgentCard `json:"agents"`
	}
	if err := json.NewDecoder(res.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Agents) != 1 || doc.Agents[0].Name != "far" {
		t.Fatalf("doc = %+v", doc)
	}
	if !strings.HasPrefix(doc.Agents[0].URL, ts.URL+"/a2a/far") {
		t.Fatalf("url = %s", doc.Agents[0].URL)
	}

	// Card route.
	cardRes, err := http.Get(ts.URL + "/a2a/far/card")
	if err != nil {
		t.Fatal(err)
	}
	defer cardRes.Body.Close()
	if cardRes.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", cardRes.StatusCode)
	}
}

func TestA2AEndToEndWithClientTool(t *testing.T) {
	srv := newServerWithAgent(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// The a2a_call tool hits the server's /a2a/far endpoint.
	args := `{"url":"` + ts.URL + `","agent":"far","message":"hello there"}`
	res, err := a2a.CallTool.Run(context.Background(), nil, []byte(args))
	if err != nil {
		t.Fatal(err)
	}
	text, ok := res.(string)
	if !ok || text != "Hello from the far side" {
		t.Fatalf("res = %v (%T)", res, res)
	}
}

func TestA2AClientToolValidation(t *testing.T) {
	_, err := a2a.CallTool.Run(context.Background(), nil, []byte(`{"url":"http://x","agent":"a"}`))
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("err = %v", err)
	}
	_, err = a2a.CallTool.Run(context.Background(), nil, []byte(`{"url":"http://127.0.0.1:1","agent":"a","message":"hi"}`))
	if err == nil {
		t.Fatal("expected connection error")
	}
}
