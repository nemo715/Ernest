package config

import (
	"strings"
	"testing"
)

// orchestrationConfig returns a minimal valid config with three mock
// agents and a memory store (no keys, no network).
func orchestrationConfig() *Config {
	return &Config{
		Agents: []AgentConfig{
			{Name: "lead", Provider: "mock", Model: "mock-1"},
			{Name: "researcher", Provider: "mock", Model: "mock-1"},
			{Name: "writer", Provider: "mock", Model: "mock-1"},
		},
		Store: StoreConfig{Type: "memory"},
	}
}

func TestValidateTeamsOK(t *testing.T) {
	c := orchestrationConfig()
	c.Teams = []TeamConfig{
		{Name: "editorial", Leader: "lead", Members: []string{"researcher", "writer"}, Process: "sequential"},
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateTeamsErrors(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"missing leader", func(c *Config) {
			c.Teams = []TeamConfig{{Name: "t", Leader: "ghost", Members: []string{"writer"}}}
		}, "unknown leader"},
		{"missing member", func(c *Config) {
			c.Teams = []TeamConfig{{Name: "t", Leader: "lead", Members: []string{"ghost"}}}
		}, "unknown member"},
		{"no members", func(c *Config) {
			c.Teams = []TeamConfig{{Name: "t", Leader: "lead"}}
		}, "at least one member"},
		{"bad process", func(c *Config) {
			c.Teams = []TeamConfig{{Name: "t", Leader: "lead", Members: []string{"writer"}, Process: "matrix"}}
		}, "unknown process"},
		{"duplicate team", func(c *Config) {
			c.Teams = []TeamConfig{
				{Name: "t", Leader: "lead", Members: []string{"writer"}},
				{Name: "t", Leader: "lead", Members: []string{"writer"}},
			}
		}, "duplicate team"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := orchestrationConfig()
			tc.mutate(c)
			err := c.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestValidateWorkflowsOK(t *testing.T) {
	c := orchestrationConfig()
	c.Workflows = []WorkflowConfig{{
		Name: "pipeline",
		Steps: []WorkflowStepConfig{
			{Name: "research", Agent: "researcher"},
			{Name: "write", Agent: "writer", DependsOn: []string{"research"}},
		},
	}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateWorkflowsErrors(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"unknown agent", func(c *Config) {
			c.Workflows = []WorkflowConfig{{Name: "w", Steps: []WorkflowStepConfig{{Name: "s", Agent: "ghost"}}}}
		}, "unknown agent"},
		{"unknown dependency", func(c *Config) {
			c.Workflows = []WorkflowConfig{{Name: "w", Steps: []WorkflowStepConfig{
				{Name: "s", Agent: "writer", DependsOn: []string{"ghost"}},
			}}}
		}, "depends on unknown step"},
		{"duplicate step", func(c *Config) {
			c.Workflows = []WorkflowConfig{{Name: "w", Steps: []WorkflowStepConfig{
				{Name: "s", Agent: "writer"},
				{Name: "s", Agent: "writer"},
			}}}
		}, "duplicate step"},
		{"guard without rubric", func(c *Config) {
			c.Workflows = []WorkflowConfig{{Name: "w", Steps: []WorkflowStepConfig{
				{Name: "s", Agent: "writer", Guard: &GuardConfig{}},
			}}}
		}, "guard requires a rubric"},
		{"cycle", func(c *Config) {
			c.Workflows = []WorkflowConfig{{Name: "w", Steps: []WorkflowStepConfig{
				{Name: "a", Agent: "writer", DependsOn: []string{"b"}},
				{Name: "b", Agent: "writer", DependsOn: []string{"a"}},
			}}}
		}, "cycle"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := orchestrationConfig()
			tc.mutate(c)
			err := c.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestBuildCreatesTeamsAndWorkflows(t *testing.T) {
	c := orchestrationConfig()
	c.Teams = []TeamConfig{
		{Name: "editorial", Leader: "lead", Members: []string{"researcher", "writer"}, Process: "sequential"},
	}
	c.Workflows = []WorkflowConfig{{
		Name: "pipeline",
		Steps: []WorkflowStepConfig{
			{Name: "research", Agent: "researcher", Prompt: "research {{input}}"},
			{Name: "write", Agent: "writer", Prompt: "write from {{research}}", DependsOn: []string{"research"}},
		},
	}}
	rt, err := c.Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	tm := rt.Teams["editorial"]
	if tm == nil {
		t.Fatal("team not built")
	}
	if tm.Process != "sequential" {
		t.Fatalf("team process = %q", tm.Process)
	}
	if tm.Name != "editorial" || len(tm.Members) != 2 {
		t.Fatalf("team shape wrong: %+v", tm)
	}

	wf := rt.Workflows["pipeline"]
	if wf == nil {
		t.Fatal("workflow not built")
	}
	if len(wf.Steps) != 2 {
		t.Fatalf("workflow steps = %d", len(wf.Steps))
	}

	// The built workflow actually runs on the mock agents.
	res, err := wf.Run(t.Context(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "completed" {
		t.Fatalf("workflow status = %s (%s)", res.Status, res.Error)
	}
	if !strings.Contains(res.Output, "\"research\"") || !strings.Contains(res.Output, "\"write\"") {
		t.Fatalf("workflow output missing steps: %s", res.Output)
	}
}

func TestBuildSequentialTeamRunsWithoutLeaderCall(t *testing.T) {
	c := orchestrationConfig()
	c.Teams = []TeamConfig{
		{Name: "chain", Leader: "lead", Members: []string{"researcher", "writer"}, Process: "sequential"},
	}
	rt, err := c.Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	res, err := rt.Teams["chain"].Chat(t.Context(), "start")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "completed" {
		t.Fatalf("status = %s (%s)", res.Status, res.Error)
	}
	if res.Metadata["process"] != "sequential" {
		t.Fatalf("metadata = %+v", res.Metadata)
	}
}
