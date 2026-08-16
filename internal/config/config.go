// Package config loads ernest.json — the declarative agent configuration
// used by the CLI and the playground server — and builds runtime agents,
// session stores and MCP connections from it.
package config

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nemo715/Ernest/internal/agent"
	"github.com/nemo715/Ernest/internal/a2a"
	"github.com/nemo715/Ernest/internal/browser"
	"github.com/nemo715/Ernest/internal/core"
	"github.com/nemo715/Ernest/internal/knowledge"
	"github.com/nemo715/Ernest/internal/llm"
	"github.com/nemo715/Ernest/internal/mcp"
	"github.com/nemo715/Ernest/internal/storage"
	"github.com/nemo715/Ernest/internal/team"
	"github.com/nemo715/Ernest/internal/vector"
	"github.com/nemo715/Ernest/internal/workflow"
)

// DefaultFile is the default config file name.
const DefaultFile = "ernest.json"

// browserToolName is the lazily-wired browser tool: it is not part of the
// core built-in registry (it carries a heavy CDP dependency), so agents
// opt in via "tools": ["browser"] and the instance only launches on
// first use.
const browserToolName = "browser"

// a2aToolName is the lazily-wired A2A client tool.
const a2aToolName = "a2a_call"

// defaultApprovalTools are the tool names that require human approval
// unless the agent's toolPolicy.autoApprove exempts them. file_write
// and every browser action are side-effectful by default; file_read,
// file_list and web_search stay safe to run unattended.
var defaultApprovalTools = map[string]bool{
	core.ToolFileWrite:    true,
	browser.NavigateName:  true,
	browser.ReadName:      true,
	browser.ClickName:     true,
	browser.TypeName:      true,
	browser.ScreenshotName: true,
}

// Config is the root document of ernest.json.
type Config struct {
	Agents     []AgentConfig     `json:"agents"`
	MCPServers []MCPServerConfig `json:"mcpServers,omitempty"`
	Store      StoreConfig       `json:"store,omitempty"`
	// Embeddings configures the provider used to embed knowledge and
	// semantic memory. When omitted, agents whose provider is "mock" use
	// a deterministic local embedder; other providers require this block.
	Embeddings EmbeddingsConfig `json:"embeddings,omitempty"`
	// Failures, when set, appends a FailureRecord (JSONL) for every
	// failed run on the server: the production feed that
	// `ernest eval --learn` turns into regression scenarios.
	Failures string `json:"failures,omitempty"`

	// Teams are declarative multi-agent teams built at load time and
	// runnable via `ernest run --team` or POST /api/teams/{name}/run.
	Teams []TeamConfig `json:"teams,omitempty"`
	// Workflows are declarative step DAGs over the configured agents,
	// runnable via `ernest run --workflow` or
	// POST /api/workflows/{name}/run.
	Workflows []WorkflowConfig `json:"workflows,omitempty"`

	// dir is the config file's directory; relative knowledge sources
	// resolve against it.
	dir string
}

// AgentConfig describes one agent.
type AgentConfig struct {
	Name          string   `json:"name"`
	Description   string   `json:"description,omitempty"`
	Provider      string   `json:"provider"`
	Model         string   `json:"model"`
	BaseURL       string   `json:"baseUrl,omitempty"`
	APIKeyEnv     string   `json:"apiKeyEnv,omitempty"`
	Instructions  string   `json:"instructions,omitempty"`
	Temperature   *float64 `json:"temperature,omitempty"`
	MaxTokens     int      `json:"maxTokens,omitempty"`
	MaxIterations int      `json:"maxIterations,omitempty"`
	Tools         []string `json:"tools,omitempty"`      // built-in tool names
	MCPServers    []string `json:"mcpServers,omitempty"` // names of configured servers
	Memory        bool     `json:"memory,omitempty"`     // persist sessions (requires store)
	// Knowledge enables retrieval-augmented generation: configured
	// sources are embedded at startup and the most relevant chunks are
	// injected into every run (and audited in the run context).
	Knowledge *KnowledgeConfig `json:"knowledge,omitempty"`
	// SemanticMemory adds the remember/recall tools backed by a
	// per-agent vector collection (in-memory by default, Qdrant when
	// store.type is qdrant).
	SemanticMemory bool `json:"semanticMemory,omitempty"`
	// ToolSandbox is the base directory for the file/shell tool packs
	// (file_read/file_write/file_list/shell_exec). Required when those
	// tools are attached; relative paths resolve against the config
	// file's directory. Every path outside it is rejected at runtime.
	ToolSandbox string `json:"toolSandbox,omitempty"`
	// ToolPolicy tunes approval behavior for the tool packs.
	ToolPolicy *ToolPolicyConfig `json:"toolPolicy,omitempty"`
}

// ToolPolicyConfig tunes approval behavior for the tool packs.
//
// Guardrails (not optional):
//   - file_write and every browser tool require human approval by
//     default; autoApprove is the explicit opt-out.
//   - shell_exec is disabled unless enableShell is true — and even then
//     it ALWAYS requires approval (autoApprove may never contain it,
//     enforced at validation) and every command is audit-logged in the
//     run trace.
//
//	"toolPolicy": {
//	  "enableShell": true,
//	  "autoApprove": ["file_write"],
//	  "requireApproval": ["file_read"]
//	}
type ToolPolicyConfig struct {
	// EnableShell opts the agent into shell_exec (default false).
	EnableShell bool `json:"enableShell,omitempty"`
	// AutoApprove lists tool names that skip the human approval gate.
	// shell_exec can never appear here.
	AutoApprove []string `json:"autoApprove,omitempty"`
	// RequireApproval lists additional tool names that always pause
	// for human approval (beyond the built-in defaults).
	RequireApproval []string `json:"requireApproval,omitempty"`
}

// EmbeddingsConfig selects the provider used to embed text.
//
// Supported providers: mock (deterministic, no key), openai,
// compatible (any OpenAI-compatible /embeddings endpoint, e.g.
// OpenRouter) and ollama.
//
//	"embeddings": {
//	  "provider": "compatible",
//	  "baseUrl": "https://openrouter.ai/api/v1",
//	  "model": "text-embedding-3-small",
//	  "apiKeyEnv": "OPENROUTER_API_KEY"
//	}
type EmbeddingsConfig struct {
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
	BaseURL   string `json:"baseUrl,omitempty"`
	APIKeyEnv string `json:"apiKeyEnv,omitempty"`
}

// KnowledgeConfig configures an agent's knowledge base.
type KnowledgeConfig struct {
	// Sources are files or directories. Directories are scanned for
	// *.md and *.txt (recursively sorted); relative paths resolve
	// against the config file's directory.
	Sources []string `json:"sources"`
	// TopK is the number of chunks retrieved per query (default 4,
	// clamped 1..20).
	TopK int `json:"topK,omitempty"`
}

// MCPServerConfig describes one MCP server connection.
type MCPServerConfig struct {
	Name      string   `json:"name"`
	Transport string   `json:"transport"` // http | stdio (default stdio)
	URL       string   `json:"url,omitempty"`
	Command   string   `json:"command,omitempty"`
	Args      []string `json:"args,omitempty"`
	Enabled   *bool    `json:"enabled,omitempty"`
}

// StoreConfig picks the session store.
type StoreConfig struct {
	Type string `json:"type,omitempty"` // memory (default) | sqlite
	DSN  string `json:"dsn,omitempty"`  // sqlite file path
}

// TeamConfig describes a declarative multi-agent team.
//
// Process "hierarchical" (default) gives the leader the delegate tool
// and full autonomy; "sequential" runs members in declaration order,
// feeding each member's output to the next (no leader model call).
//
//	"teams": [{
//	  "name": "editorial",
//	  "leader": "lead",
//	  "members": ["researcher", "writer"],
//	  "process": "hierarchical"
//	}]
type TeamConfig struct {
	Name          string   `json:"name"`
	Description   string   `json:"description,omitempty"`
	Leader        string   `json:"leader"`
	Members       []string `json:"members"`
	Process       string   `json:"process,omitempty"` // hierarchical (default) | sequential
	MaxIterations int      `json:"maxIterations,omitempty"`
	Instructions  string   `json:"instructions,omitempty"`
}

// GuardConfig attaches an LLM-judged quality gate to a workflow step:
// the step's agent provider scores the step output against the rubric
// (0..1) and the step fails below minScore (default 0.7). The judge is
// a second model call — the mock provider cannot judge; use a real (or
// scripted) model for guarded steps.
type GuardConfig struct {
	Rubric   string  `json:"rubric"`
	MinScore float64 `json:"minScore,omitempty"`
}

// WorkflowStepConfig is one node of a workflow DAG. Prompt placeholders
// of the form {{name}} (or {{input}}) are replaced with earlier step
// outputs before the agent call.
type WorkflowStepConfig struct {
	Name      string       `json:"name"`
	Agent     string       `json:"agent"`
	Prompt    string       `json:"prompt,omitempty"`
	DependsOn []string     `json:"dependsOn,omitempty"`
	Retries   int          `json:"retries,omitempty"`
	Guard     *GuardConfig `json:"guard,omitempty"`
}

// WorkflowConfig describes a declarative step DAG over the configured
// agents. Steps run in dependency order; independent steps run
// concurrently; the final result is the shared state as JSON.
type WorkflowConfig struct {
	Name        string               `json:"name"`
	Description string               `json:"description,omitempty"`
	Steps       []WorkflowStepConfig `json:"steps"`
	MaxRetries  int                  `json:"maxRetries,omitempty"`
}

// Load reads and validates the config file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	c.dir = filepath.Dir(path)
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return &c, nil
}

// Validate checks the document shape: agent fields, tool names, MCP
// references and store type.
func (c *Config) Validate() error {
	if len(c.Agents) == 0 {
		return fmt.Errorf("no agents defined")
	}
	switch strings.ToLower(c.Store.Type) {
	case "", "memory", "inmem", "in-memory", "sqlite", "qdrant":
	default:
		return fmt.Errorf("unknown store type %q (memory|sqlite|qdrant)", c.Store.Type)
	}
	seen := map[string]bool{}
	for _, a := range c.Agents {
		if a.Name == "" {
			return fmt.Errorf("agent without name")
		}
		if seen[a.Name] {
			return fmt.Errorf("duplicate agent %q", a.Name)
		}
		seen[a.Name] = true
		if a.Provider == "" {
			return fmt.Errorf("agent %q: provider is required", a.Name)
		}
		if a.Model == "" && !strings.EqualFold(a.Provider, "mock") {
			return fmt.Errorf("agent %q: model is required", a.Name)
		}
		for _, t := range a.Tools {
			if t == browserToolName || t == a2aToolName ||
				t == browser.NavigateName || t == browser.ReadName ||
				t == browser.ClickName || t == browser.TypeName ||
				t == browser.ScreenshotName ||
				t == agent.RememberToolName || t == agent.RecallToolName {
				continue // wired lazily in buildAgent (needs no registry entry)
			}
			if core.ToolsByName(core.BuiltinTools)[t] == nil {
				return fmt.Errorf("agent %q: unknown tool %q", a.Name, t)
			}
		}
		if err := validateToolPolicy(a); err != nil {
			return err
		}
	}
	// Knowledge + semantic memory rules.
	for _, a := range c.Agents {
		if a.Knowledge != nil {
			if len(a.Knowledge.Sources) == 0 {
				return fmt.Errorf("agent %q: knowledge requires at least one source", a.Name)
			}
			if a.Knowledge.TopK < 0 || a.Knowledge.TopK > 20 {
				return fmt.Errorf("agent %q: knowledge topK must be 1..20", a.Name)
			}
		}
	}
	if err := c.validateEmbeddings(); err != nil {
		return err
	}
	mcpSeen := map[string]bool{}
	for _, m := range c.MCPServers {
		if m.Name == "" {
			return fmt.Errorf("mcp server without name")
		}
		if mcpSeen[m.Name] {
			return fmt.Errorf("duplicate mcp server %q", m.Name)
		}
		mcpSeen[m.Name] = true
		switch m.Transport {
		case "", "stdio":
			if m.Command == "" {
				return fmt.Errorf("mcp server %q: stdio transport requires command", m.Name)
			}
		case "http":
			if m.URL == "" {
				return fmt.Errorf("mcp server %q: http transport requires url", m.Name)
			}
		default:
			return fmt.Errorf("mcp server %q: unknown transport %q (stdio|http)", m.Name, m.Transport)
		}
	}
	for _, a := range c.Agents {
		for _, name := range a.MCPServers {
			if !mcpSeen[name] {
				return fmt.Errorf("agent %q: unknown mcp server %q", a.Name, name)
			}
		}
	}
	if err := c.validateTeams(seen); err != nil {
		return err
	}
	if err := c.validateWorkflows(seen); err != nil {
		return err
	}
	return nil
}

// validateToolPolicy enforces the tool pack guardrails for one agent:
// the sandbox requirement, the shell opt-in, and toolPolicy tool-name
// sanity.
func validateToolPolicy(a AgentConfig) error {
	attached := map[string]bool{}
	for _, t := range a.Tools {
		attached[t] = true
	}
	policy := a.ToolPolicy
	if policy == nil {
		policy = &ToolPolicyConfig{}
	}
	// file/shell packs only run inside a configured sandbox.
	if attached[core.ToolFileRead] || attached[core.ToolFileWrite] || attached[core.ToolFileList] || attached[core.ToolShellExec] {
		if strings.TrimSpace(a.ToolSandbox) == "" {
			return fmt.Errorf("agent %q: file/shell tools require \"toolSandbox\" (the pack base directory)", a.Name)
		}
	}
	// shell_exec is disabled by default; enabling it is explicit.
	if attached[core.ToolShellExec] && !policy.EnableShell {
		return fmt.Errorf("agent %q: shell_exec is disabled by default — set toolPolicy.enableShell to opt in (commands always require human approval and are audit-logged)", a.Name)
	}
	for _, t := range policy.AutoApprove {
		if t == core.ToolShellExec {
			return fmt.Errorf("agent %q: shell_exec always requires human approval and can never appear in toolPolicy.autoApprove", a.Name)
		}
		if !knownToolName(t) {
			return fmt.Errorf("agent %q: toolPolicy.autoApprove references unknown tool %q", a.Name, t)
		}
	}
	for _, t := range policy.RequireApproval {
		if !knownToolName(t) {
			return fmt.Errorf("agent %q: toolPolicy.requireApproval references unknown tool %q", a.Name, t)
		}
		// The tool must actually be attached (or auto-attached via
		// semantic memory) for the policy to mean anything.
		if !attached[t] && !((t == agent.RememberToolName || t == agent.RecallToolName) && a.SemanticMemory) {
			return fmt.Errorf("agent %q: toolPolicy.requireApproval references tool %q which is not attached to this agent", a.Name, t)
		}
	}
	return nil
}

// knownToolName reports whether t is a valid tool name: a core built-in,
// a browser pack tool, the legacy browser tool, the A2A client or the
// memory tools.
func knownToolName(t string) bool {
	if core.ToolsByName(core.BuiltinTools)[t] != nil {
		return true
	}
	switch t {
	case browserToolName, a2aToolName,
		browser.NavigateName, browser.ReadName, browser.ClickName,
		browser.TypeName, browser.ScreenshotName,
		agent.RememberToolName, agent.RecallToolName:
		return true
	}
	return false
}

// validateTeams checks team shapes: unique names, existing agents and
// a known process mode.
func (c *Config) validateTeams(agents map[string]bool) error {
	teamSeen := map[string]bool{}
	for _, t := range c.Teams {
		if t.Name == "" {
			return fmt.Errorf("team without name")
		}
		if teamSeen[t.Name] {
			return fmt.Errorf("duplicate team %q", t.Name)
		}
		teamSeen[t.Name] = true
		if !agents[t.Leader] {
			return fmt.Errorf("team %q: unknown leader %q", t.Name, t.Leader)
		}
		if len(t.Members) == 0 {
			return fmt.Errorf("team %q: at least one member is required", t.Name)
		}
		for _, m := range t.Members {
			if !agents[m] {
				return fmt.Errorf("team %q: unknown member %q", t.Name, m)
			}
		}
		switch strings.ToLower(t.Process) {
		case "", "hierarchical", "sequential":
		default:
			return fmt.Errorf("team %q: unknown process %q (hierarchical|sequential)", t.Name, t.Process)
		}
	}
	return nil
}

// validateWorkflows checks workflow shapes: unique names and steps,
// existing agents, valid dependencies and no cycles.
func (c *Config) validateWorkflows(agents map[string]bool) error {
	wfSeen := map[string]bool{}
	for _, w := range c.Workflows {
		if w.Name == "" {
			return fmt.Errorf("workflow without name")
		}
		if wfSeen[w.Name] {
			return fmt.Errorf("duplicate workflow %q", w.Name)
		}
		wfSeen[w.Name] = true
		stepNames := map[string]bool{}
		for _, s := range w.Steps {
			if s.Name == "" {
				return fmt.Errorf("workflow %q: step without name", w.Name)
			}
			if stepNames[s.Name] {
				return fmt.Errorf("workflow %q: duplicate step %q", w.Name, s.Name)
			}
			stepNames[s.Name] = true
			if !agents[s.Agent] {
				return fmt.Errorf("workflow %q: step %q: unknown agent %q", w.Name, s.Name, s.Agent)
			}
			if s.Guard != nil && s.Guard.Rubric == "" {
				return fmt.Errorf("workflow %q: step %q: guard requires a rubric", w.Name, s.Name)
			}
		}
		for _, s := range w.Steps {
			for _, d := range s.DependsOn {
				if !stepNames[d] {
					return fmt.Errorf("workflow %q: step %q depends on unknown step %q", w.Name, s.Name, d)
				}
			}
		}
		if hasCycle(w.Steps) {
			return fmt.Errorf("workflow %q: dependency cycle detected", w.Name)
		}
	}
	return nil
}

// hasCycle reports whether the step DAG contains a dependency cycle
// (Kahn's topological-sort check).
func hasCycle(steps []WorkflowStepConfig) bool {
	indeg := map[string]int{}
	deps := map[string][]string{}
	names := make([]string, 0, len(steps))
	for _, s := range steps {
		names = append(names, s.Name)
		indeg[s.Name] = len(s.DependsOn)
		for _, d := range s.DependsOn {
			deps[d] = append(deps[d], s.Name)
		}
	}
	queue := []string{}
	for _, n := range names {
		if indeg[n] == 0 {
			queue = append(queue, n)
		}
	}
	processed := 0
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		processed++
		for _, next := range deps[n] {
			indeg[next]--
			if indeg[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	return processed != len(names)
}

// validateEmbeddings checks the embeddings block against the agents
// that need it (knowledge or semantic memory).
func (c *Config) validateEmbeddings() error {
	need := false
	for _, a := range c.Agents {
		if a.Knowledge != nil || a.SemanticMemory {
			need = true
		}
	}
	if !need {
		return nil
	}
	switch strings.ToLower(c.Embeddings.Provider) {
	case "":
		// No explicit embeddings config: allowed only when every agent
		// that needs embeddings runs on the mock provider (deterministic
		// local embedder, no key required).
		for _, a := range c.Agents {
			if (a.Knowledge != nil || a.SemanticMemory) && !strings.EqualFold(a.Provider, "mock") {
				return fmt.Errorf(
					"agent %q uses knowledge/semanticMemory but no \"embeddings\" block is set "+
						"(add embeddings: {provider: \"compatible\", baseUrl: ..., model: ..., apiKeyEnv: ...})",
					a.Name)
			}
		}
		return nil
	case "mock":
		return nil
	case "openai", "compatible", "ollama":
		if strings.EqualFold(c.Embeddings.Provider, "compatible") && c.Embeddings.BaseURL == "" {
			return fmt.Errorf("embeddings: provider compatible requires baseUrl")
		}
		return nil
	default:
		return fmt.Errorf("embeddings: unknown provider %q (mock|openai|compatible|ollama)", c.Embeddings.Provider)
	}
}
type Runtime struct {
	Agents []*agent.Agent
	Store  storage.SessionStore
	// Teams and Workflows are the config-declared orchestrations, keyed
	// by name (empty maps when the config declares none).
	Teams     map[string]*team.Team
	Workflows map[string]*workflow.Workflow
	clients   []*mcp.Client
}

// Build constructs agents, the session store and MCP clients from the
// config. env resolves environment variables (os.Getenv by default).
// Close must be called to release stores and MCP connections.
func (c *Config) Build(env func(string) string) (*Runtime, error) {
	if env == nil {
		env = os.Getenv
	}
	rt := &Runtime{}
	var err error
	switch strings.ToLower(c.Store.Type) {
	case "", "memory", "inmem", "in-memory":
		rt.Store = storage.NewInMemoryStore()
	case "sqlite":
		dsn := c.Store.DSN
		if dsn == "" {
			dsn = "ernest.db"
		}
		rt.Store, err = storage.NewSQLiteStore(dsn)
		if err != nil {
			return nil, err
		}
	}

	// MCP clients (named, so agents can reference them).
	clients := map[string]*mcp.Client{}
	for _, mc := range c.MCPServers {
		if mc.Enabled != nil && !*mc.Enabled {
			continue
		}
		var cl *mcp.Client
		switch mc.Transport {
		case "", "stdio":
			cl, err = mcp.NewStdio(mc.Command, mc.Args, mcp.Options{Name: "ernest"})
		case "http":
			cl, err = mcp.NewHTTP(mc.URL, mcp.Options{Name: "ernest"})
		}
		if err != nil {
			rt.Close()
			return nil, fmt.Errorf("mcp server %q: %w", mc.Name, err)
		}
		clients[mc.Name] = cl
		rt.clients = append(rt.clients, cl)
	}

	for _, ac := range c.Agents {
		ag, err := c.buildAgent(ac, env, rt.Store, clients)
		if err != nil {
			rt.Close()
			return nil, err
		}
		rt.Agents = append(rt.Agents, ag)
	}
	if err := rt.buildOrchestration(c); err != nil {
		rt.Close()
		return nil, err
	}
	return rt, nil
}

// buildOrchestration builds the config-declared teams and workflows
// from the runtime agents.
func (rt *Runtime) buildOrchestration(c *Config) error {
	rt.Teams = map[string]*team.Team{}
	rt.Workflows = map[string]*workflow.Workflow{}
	agentByName := map[string]*agent.Agent{}
	for _, a := range rt.Agents {
		agentByName[a.Name] = a
	}
	for _, tc := range c.Teams {
		members := make([]*agent.Agent, 0, len(tc.Members))
		for _, m := range tc.Members {
			members = append(members, agentByName[m])
		}
		t := team.New(tc.Name, agentByName[tc.Leader], members...)
		t.Description = tc.Description
		t.Instructions = tc.Instructions
		if tc.MaxIterations > 0 {
			t.MaxIterations = tc.MaxIterations
		}
		if strings.EqualFold(tc.Process, "sequential") {
			t.Process = "sequential"
		} else {
			t.Process = "hierarchical"
		}
		rt.Teams[tc.Name] = t
	}
	for _, wc := range c.Workflows {
		specs := make([]workflow.StepSpec, 0, len(wc.Steps))
		for _, sc := range wc.Steps {
			sp := workflow.StepSpec{
				Name:      sc.Name,
				Agent:     sc.Agent,
				Prompt:    sc.Prompt,
				DependsOn: sc.DependsOn,
				Retries:   sc.Retries,
			}
			if sc.Guard != nil {
				sp.Guard = &workflow.GuardSpec{Rubric: sc.Guard.Rubric, MinScore: sc.Guard.MinScore}
			}
			specs = append(specs, sp)
		}
		wf, err := workflow.Build(wc.Name, specs, agentByName, wc.MaxRetries)
		if err != nil {
			return err
		}
		wf.Description = wc.Description
		rt.Workflows[wc.Name] = wf
	}
	return nil
}

// Close releases MCP connections and the session store.
func (rt *Runtime) Close() error {
	var errs []string
	for _, cl := range rt.clients {
		if err := cl.Close(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if rt.Store != nil {
		if err := rt.Store.Close(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("close: %s", strings.Join(errs, "; "))
	}
	return nil
}

// approvalTools computes the agent's require-approval tool list: the
// built-in default gates (file_write + browser pack) minus the policy's
// autoApprove exemptions, plus the policy's extra requireApproval
// entries. shell_exec is always required when attached — it can never
// be auto-approved (enforced at validation).
func approvalTools(ac AgentConfig) []string {
	attached := map[string]bool{}
	for _, t := range ac.Tools {
		attached[t] = true
	}
	exempt := map[string]bool{}
	var extra []string
	if ac.ToolPolicy != nil {
		for _, t := range ac.ToolPolicy.AutoApprove {
			exempt[t] = true
		}
		extra = ac.ToolPolicy.RequireApproval
	}
	var out []string
	seen := map[string]bool{}
	add := func(t string) {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	for _, t := range ac.Tools {
		if t == core.ToolShellExec || (defaultApprovalTools[t] && !exempt[t]) {
			add(t)
		}
	}
	for _, t := range extra {
		if attached[t] {
			// An explicit requireApproval always wins over autoApprove.
			add(t)
		}
	}
	return out
}

// resolveSandbox resolves the agent's toolSandbox against the config
// file's directory so runtime behavior is independent of the process
// CWD. An empty sandbox stays empty (file/shell tools refuse to run).
func (c *Config) resolveSandbox(sandbox string) string {
	if strings.TrimSpace(sandbox) == "" {
		return ""
	}
	if filepath.IsAbs(sandbox) {
		return sandbox
	}
	return filepath.Join(c.dir, sandbox)
}

func (c *Config) buildAgent(ac AgentConfig, env func(string) string, store storage.SessionStore, clients map[string]*mcp.Client) (*agent.Agent, error) {
	p, err := c.provider(ac, env)
	if err != nil {
		return nil, err
	}
	ag := agent.New(ac.Name, p)
	ag.Description = ac.Description
	ag.Instructions = ac.Instructions
	ag.Temperature = ac.Temperature
	ag.MaxTokens = ac.MaxTokens
	ag.MaxIterations = ac.MaxIterations
	ag.ToolSandbox = c.resolveSandbox(ac.ToolSandbox)
	ag.RequireApprovalTools = approvalTools(ac)
	if len(ac.Tools) > 0 {
		byName := core.ToolsByName(core.BuiltinTools)
		browserByName := core.ToolsByName(browser.Tools)
		for _, name := range ac.Tools {
			switch name {
			case browserToolName:
				ag.Tools = append(ag.Tools, browser.Tool)
			case browser.NavigateName, browser.ReadName, browser.ClickName, browser.TypeName, browser.ScreenshotName:
				ag.Tools = append(ag.Tools, browserByName[name])
			case a2aToolName:
				ag.Tools = append(ag.Tools, a2a.CallTool)
			default:
				ag.Tools = append(ag.Tools, byName[name])
			}
		}
	}
	if ac.Memory {
		ag.Store = store
	}
	// Knowledge base: embed sources at startup and attach the KB so the
	// run loop injects the most relevant chunks into every request.
	if ac.Knowledge != nil || ac.SemanticMemory {
		emb, err := c.embedder(ac, env)
		if err != nil {
			return nil, err
		}
		vstore := c.vectorStore()
		if ac.Knowledge != nil {
			kb := knowledge.New(vstore, emb, knowledge.ChunkOptions{})
			kb.SetCollection("ernest_knowledge_" + ac.Name)
			kb.TopK = ac.Knowledge.TopK
			if kb.TopK <= 0 {
				kb.TopK = 4
			}
			if err := c.ingestKnowledge(context.Background(), kb, ac.Knowledge.Sources); err != nil {
				return nil, fmt.Errorf("agent %q: %w", ac.Name, err)
			}
			ag.Knowledge = kb
		}
		if ac.SemanticMemory {
			kb := knowledge.New(vstore, emb, knowledge.ChunkOptions{ChunkSize: 400})
			kb.SetCollection("ernest_memory_" + ac.Name)
			ag.SemanticMemory = kb
			ag.Tools = append(ag.Tools, agent.MemoryTools(kb)...)
		}
	}
	for _, name := range ac.MCPServers {
		cl := clients[name]
		if cl == nil {
			continue
		}
		tools, err := cl.AsCoreTools(context.Background())
		if err != nil {
			return nil, fmt.Errorf("agent %q: mcp server %q: %w", ac.Name, name, err)
		}
		ag.Tools = append(ag.Tools, tools...)
	}
	return ag, nil
}

// embedder builds the embeddings provider for knowledge and semantic
// memory. When the embeddings block is omitted, the agent's own
// provider is used as the default (mock -> deterministic local
// embedder).
func (c *Config) embedder(ac AgentConfig, env func(string) string) (llm.Embedder, error) {
	ec := c.Embeddings
	provider := strings.ToLower(ec.Provider)
	if provider == "" {
		provider = strings.ToLower(ac.Provider)
	}
	switch provider {
	case "mock", "mock-1":
		return llm.NewMock(llm.MockConfig{}), nil
	case "openai":
		keyEnv := ec.APIKeyEnv
		if keyEnv == "" {
			keyEnv = "OPENAI_API_KEY"
		}
		key := env(keyEnv)
		if key == "" {
			return nil, fmt.Errorf("embeddings: %s not set (set the env var or embeddings.apiKeyEnv)", keyEnv)
		}
		model := ec.Model
		if model == "" {
			model = "text-embedding-3-small"
		}
		p, _ := llm.OpenAI(key, model).(llm.Embedder)
		return p, nil
	case "compatible":
		if ec.BaseURL == "" {
			return nil, fmt.Errorf("embeddings: provider compatible requires baseUrl")
		}
		model := ec.Model
		if model == "" {
			model = "text-embedding-3-small"
		}
		key := ""
		if ec.APIKeyEnv != "" {
			key = env(ec.APIKeyEnv)
		}
		p, _ := llm.NewOpenAICompat(llm.OpenAICompatConfig{
			BaseURL:   ec.BaseURL,
			APIKey:    key,
			Model:     model,
			EmbedModel: model,
		}).(llm.Embedder)
		return p, nil
	case "ollama":
		p, _ := llm.Ollama(ec.BaseURL, ec.Model).(llm.Embedder)
		return p, nil
	default:
		return nil, fmt.Errorf("embeddings: unknown provider %q (mock|openai|compatible|ollama)", ec.Provider)
	}
}

// vectorStore builds the vector store for knowledge and semantic
// memory: Qdrant when store.type is qdrant (dsn = base URL), otherwise
// an in-memory store.
func (c *Config) vectorStore() vector.VectorStore {
	if strings.EqualFold(c.Store.Type, "qdrant") {
		return vector.NewQdrantStore(vector.QdrantConfig{BaseURL: c.Store.DSN})
	}
	return vector.NewInMemoryStore()
}

// ingestKnowledge embeds and stores every configured source. Relative
// paths resolve against the config file's directory.
func (c *Config) ingestKnowledge(ctx context.Context, kb *knowledge.KnowledgeBase, sources []string) error {
	for _, src := range sources {
		p := src
		if !filepath.IsAbs(p) {
			p = filepath.Join(c.dir, p)
		}
		info, err := os.Stat(p)
		if err != nil {
			return fmt.Errorf("knowledge source %q: %w", src, err)
		}
		if !info.IsDir() {
			if _, err := kb.AddFile(ctx, p, nil); err != nil {
				return fmt.Errorf("knowledge %q: %w", p, err)
			}
			continue
		}
		var files []string
		for _, pattern := range []string{"*.md", "*.txt"} {
			m, err := filepath.Glob(filepath.Join(p, pattern))
			if err != nil {
				return err
			}
			files = append(files, m...)
		}
		sort.Strings(files)
		if len(files) == 0 {
			return fmt.Errorf("knowledge source %q: no *.md or *.txt files found", src)
		}
		for _, f := range files {
			if _, err := kb.AddFile(ctx, f, nil); err != nil {
				return fmt.Errorf("knowledge %q: %w", f, err)
			}
		}
	}
	return nil
}

// provider resolves the LLM provider, reading API keys from env vars
// (the agent's apiKeyEnv override, then the provider's default).
func (c *Config) provider(ac AgentConfig, env func(string) string) (llm.Provider, error) {
	keyEnv := func() string {
		if ac.APIKeyEnv != "" {
			if v := env(ac.APIKeyEnv); v != "" {
				return v
			}
		}
		return env(defaultKeyEnv(ac.Provider))
	}
	key := func() (string, error) {
		k := keyEnv()
		if k == "" {
			return "", fmt.Errorf("agent %q: %s not set (set the env var or apiKeyEnv)", ac.Name, defaultKeyEnv(ac.Provider))
		}
		return k, nil
	}
	switch strings.ToLower(ac.Provider) {
	case "mock":
		return llm.NewMock(llm.MockConfig{Model: ac.Model}), nil
	case "openai":
		k, err := key()
		if err != nil {
			return nil, err
		}
		return llm.OpenAI(k, ac.Model), nil
	case "groq":
		k, err := key()
		if err != nil {
			return nil, err
		}
		return llm.Groq(k, ac.Model), nil
	case "anthropic":
		k, err := key()
		if err != nil {
			return nil, err
		}
		return llm.NewAnthropic(llm.AnthropicConfig{APIKey: k, Model: ac.Model, BaseURL: ac.BaseURL}), nil
	case "gemini":
		k, err := key()
		if err != nil {
			return nil, err
		}
		return llm.NewGemini(llm.GeminiConfig{APIKey: k, Model: ac.Model, BaseURL: ac.BaseURL}), nil
	case "ollama":
		return llm.Ollama(ac.BaseURL, ac.Model), nil
	case "compatible":
		if ac.BaseURL == "" {
			return nil, fmt.Errorf("agent %q: provider compatible requires baseUrl", ac.Name)
		}
		k := keyEnv() // some OpenAI-compatible endpoints need no key
		return llm.NewOpenAICompat(llm.OpenAICompatConfig{BaseURL: ac.BaseURL, APIKey: k, Model: ac.Model}), nil
	default:
		return nil, fmt.Errorf("agent %q: unknown provider %q (mock|openai|anthropic|gemini|groq|ollama|compatible)", ac.Name, ac.Provider)
	}
}

// defaultKeyEnv returns the conventional env var for a provider.
func defaultKeyEnv(provider string) string {
	switch strings.ToLower(provider) {
	case "openai":
		return "OPENAI_API_KEY"
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "gemini":
		return "GEMINI_API_KEY"
	case "groq":
		return "GROQ_API_KEY"
	}
	return ""
}
