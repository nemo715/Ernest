// Package workflow is the public API for step DAGs with guards, retries
// and agents. It forwards to the implementation in ernest/internal/workflow.
package workflow

import (
	"context"

	"github.com/nemo715/Ernest/core"
	internal "github.com/nemo715/Ernest/internal/workflow"
)

type (
	Workflow    = internal.Workflow
	Step        = internal.Step
	StepContext = internal.StepContext
)

// New builds a workflow with the given steps.
func New(name string, steps ...*Step) *Workflow {
	return internal.New(name, steps...)
}

// Run runs the workflow and returns the final result.
func Run(ctx context.Context, w *Workflow, input string) (*core.RunResult, error) {
	return w.Run(ctx, input)
}
