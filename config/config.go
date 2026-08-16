// Package config is the public API for ernest.json configuration:
// loading, validation and building runtimes with agents, teams and
// workflows. It forwards to the implementation in ernest/internal/config.
package config

import (
	"github.com/nemo715/Ernest/agent"
	"github.com/nemo715/Ernest/storage"
	"github.com/nemo715/Ernest/team"
	"github.com/nemo715/Ernest/workflow"
	internal "github.com/nemo715/Ernest/internal/config"
)

type (
	Config             = internal.Config
	AgentConfig        = internal.AgentConfig
	EmbeddingsConfig   = internal.EmbeddingsConfig
	KnowledgeConfig    = internal.KnowledgeConfig
	MCPServerConfig    = internal.MCPServerConfig
	StoreConfig        = internal.StoreConfig
	TeamConfig         = internal.TeamConfig
	GuardConfig        = internal.GuardConfig
	WorkflowStepConfig = internal.WorkflowStepConfig
	WorkflowConfig     = internal.WorkflowConfig
)

// DefaultFile is the default config file name ("ernest.json").
const DefaultFile = internal.DefaultFile

// Load reads and validates the config file at path.
func Load(path string) (*Config, error) {
	return internal.Load(path)
}

// Runtime is a built configuration: the live agents, the session store
// and the config-declared teams/workflows keyed by name.
type Runtime struct {
	Agents    []*agent.Agent
	Store     storage.SessionStore
	Teams     map[string]*team.Team
	Workflows map[string]*workflow.Workflow

	closeFn func() error
}

// Close releases the session store and MCP connections.
func (r *Runtime) Close() error {
	if r.closeFn != nil {
		return r.closeFn()
	}
	return nil
}

// Build constructs agents, the session store and orchestrations from
// the config (env resolves environment variables; nil means os.Getenv).
func Build(c *Config, env func(string) string) (*Runtime, error) {
	rt, err := c.Build(env)
	if err != nil {
		return nil, err
	}
	return &Runtime{
		Agents:    rt.Agents,
		Store:     rt.Store,
		Teams:     rt.Teams,
		Workflows: rt.Workflows,
		closeFn:   rt.Close,
	}, nil
}
