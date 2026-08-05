// Package workflow implements step-based agentic workflows: an explicit
// DAG of typed steps with guards, retries, timeouts and parallel
// execution of independent branches.
package workflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"ernest/internal/agent"
	"ernest/internal/core"
)

// Step is one node in a workflow graph.
type Step struct {
	Name      string
	DependsOn []string // step names that must finish first
	// Guard decides whether the step should run at all.
	Guard func(ctx *StepContext) (bool, error)
	// Run performs the step's work.
	Run func(ctx *StepContext) error
	// Retries overrides the workflow default for this step.
	Retries int
	// Timeout overrides the workflow default for this step.
	Timeout time.Duration
}

// StepContext is passed to every step.
type StepContext struct {
	wf    *Workflow
	mu    sync.Mutex
	State map[string]any
	Step  string
	// Ctx is the (possibly timeout-scoped) run context.
	Ctx context.Context
	// RunID identifies this execution.
	RunID string
	// Emit streams custom events into the workflow run.
	Emit func(core.RunEvent)
}

// Workflow executes steps in dependency order. Independent steps run
// concurrently; guards gate execution; failures retry and then abort.
type Workflow struct {
	Name        string
	Description string
	Steps       []*Step
	MaxRetries  int
	Timeout     time.Duration
	// Agents are named agents steps may invoke via ctx.Agent().
	Agents map[string]*agent.Agent
	// InitialState seeds the shared state.
	InitialState map[string]any
}

// New builds a workflow.
func New(name string, steps ...*Step) *Workflow {
	return &Workflow{Name: name, Steps: steps, MaxRetries: 2}
}

// Run executes the workflow. input is available to steps via ctx.State["input"].
func (wf *Workflow) Run(ctx context.Context, input any) (*core.RunResult, error) {
	return wf.runWithEmitter(ctx, input, nil)
}

// Stream executes the workflow, streaming step events to the channel.
func (wf *Workflow) Stream(ctx context.Context, input any) (<-chan core.RunEvent, error) {
	ch := make(chan core.RunEvent, 256)
	go func() {
		defer close(ch)
		_, _ = wf.runWithEmitter(ctx, input, func(ev core.RunEvent) { ch <- ev })
	}()
	return ch, nil
}

func (wf *Workflow) runWithEmitter(ctx context.Context, input any, emit func(core.RunEvent)) (*core.RunResult, error) {
	if wf.Name == "" {
		wf.Name = "workflow"
	}
	runID := newID("wf")
	if emit != nil {
		emit(core.RunEvent{Type: core.EventRunStart, RunID: runID, Agent: wf.Name})
	}
	started := time.Now()
	state := map[string]any{}
	for k, v := range wf.InitialState {
		state[k] = v
	}
	state["input"] = input

	sc := &StepContext{wf: wf, State: state, RunID: runID, Emit: emit}

	// Validate + index steps.
	if len(wf.Steps) == 0 {
		return nil, core.NewError(core.KindAgent, "workflow "+wf.Name+" has no steps")
	}
	byName := map[string]*Step{}
	for _, s := range wf.Steps {
		if s == nil || s.Name == "" {
			return nil, core.NewError(core.KindAgent, "workflow step without name")
		}
		if _, dup := byName[s.Name]; dup {
			return nil, core.NewError(core.KindAgent, "duplicate step name "+s.Name)
		}
		byName[s.Name] = s
	}
	for _, s := range wf.Steps {
		for _, dep := range s.DependsOn {
			if _, ok := byName[dep]; !ok {
				return nil, core.NewError(core.KindAgent, fmt.Sprintf("step %q depends on unknown step %q", s.Name, dep))
			}
		}
	}

	// Scheduler state.
	var mu sync.Mutex
	remaining := map[string]int{} // steps not yet started, with their dep counts
	indegree := map[string]int{}
	for _, s := range wf.Steps {
		indegree[s.Name] = len(s.DependsOn)
		remaining[s.Name] = len(s.DependsOn)
	}
	var failed error

	emitStep := func(ev core.RunEvent) {
		if emit != nil {
			emit(ev)
		}
	}

	// markDone unlocks dependents of a finished step.
	markDone := func(name string) {
		mu.Lock()
		defer mu.Unlock()
		for _, s := range wf.Steps {
			if s.Name == name {
				continue
			}
			if !containsStr(s.DependsOn, name) {
				continue
			}
			remaining[s.Name]--
			if remaining[s.Name] < 0 {
				remaining[s.Name] = 0
			}
		}
	}

	// runStep executes one step: guard -> run (with retries) -> markDone.
	runStep := func(s *Step) {
		emitStep(core.RunEvent{Type: core.EventStepStart, RunID: runID, Agent: wf.Name, Step: s.Name})

		if s.Guard != nil {
			sc.mu.Lock()
			sc.Step = s.Name
			sc.mu.Unlock()
			ok, err := s.Guard(sc)
			if err != nil {
				mu.Lock()
				if failed == nil {
					failed = core.NewError(core.KindAgent, fmt.Sprintf("guard %q failed: %v", s.Name, err), err)
				}
				mu.Unlock()
				emitStep(core.RunEvent{Type: core.EventRunError, RunID: runID, Agent: wf.Name, Step: s.Name, Error: err.Error()})
				return
			}
			if !ok {
				emitStep(core.RunEvent{Type: core.EventStepEnd, RunID: runID, Agent: wf.Name, Step: s.Name, Data: json.RawMessage(`{"skipped":true}`)})
				markDone(s.Name)
				return
			}
		}

		stepCtx, cancel := ctx, func() {}
		timeout := s.Timeout
		if timeout == 0 {
			timeout = wf.Timeout
		}
		if timeout > 0 {
			stepCtx, cancel = context.WithTimeout(ctx, timeout)
		}
		defer cancel()

		maxRetries := s.Retries
		if maxRetries == 0 {
			maxRetries = wf.MaxRetries
		}
		var runErr error
		for attempt := 0; attempt <= maxRetries; attempt++ {
			if attempt > 0 {
				select {
				case <-stepCtx.Done():
					runErr = stepCtx.Err()
					goto attemptsDone
				case <-time.After(time.Duration(attempt) * 300 * time.Millisecond):
				}
			}
			sc.mu.Lock()
			sc.Step = s.Name
			sc.Ctx = stepCtx
			sc.mu.Unlock()
			runErr = s.Run(sc)
			if runErr == nil || !core.Retryable(runErr) {
				break
			}
		}
	attemptsDone:
		if runErr != nil {
			mu.Lock()
			if failed == nil {
				failed = core.NewError(core.KindAgent, fmt.Sprintf("step %q failed: %v", s.Name, runErr), runErr)
			}
			mu.Unlock()
			emitStep(core.RunEvent{Type: core.EventRunError, RunID: runID, Agent: wf.Name, Step: s.Name, Error: runErr.Error()})
			return
		}
		emitStep(core.RunEvent{Type: core.EventStepEnd, RunID: runID, Agent: wf.Name, Step: s.Name})
		markDone(s.Name)
	}

	// Main scheduler loop.
	for {
		mu.Lock()
		if failed != nil {
			mu.Unlock()
			break
		}
		var ready []*Step
		for _, s := range wf.Steps {
			if _, started := remaining[s.Name]; !started {
				continue
			}
			if remaining[s.Name] == 0 {
				ready = append(ready, s)
				delete(remaining, s.Name) // claim it
			}
		}
		mu.Unlock()

		if len(ready) == 0 {
			mu.Lock()
			left := len(remaining)
			mu.Unlock()
			if left > 0 {
				// Nothing ready but steps remain: cycle or unreachable.
				failed = core.NewError(core.KindAgent, "workflow deadlock: dependency cycle or unreachable steps")
			}
			break
		}

		if len(ready) == 1 {
			runStep(ready[0])
		} else {
			var wg sync.WaitGroup
			for _, s := range ready {
				wg.Add(1)
				go func(s *Step) {
					defer wg.Done()
					runStep(s)
				}(s)
			}
			wg.Wait()
		}
	}

	if failed != nil {
		res := &core.RunResult{
			RunID: runID, Status: core.RunStatusFailed, Error: failed.Error(),
			DurationMS: time.Since(started).Milliseconds(),
			Metadata:   map[string]any{"workflow": wf.Name},
		}
		emitStep(core.RunEvent{Type: core.EventRunError, RunID: runID, Agent: wf.Name, Error: failed.Error()})
		emitStep(core.RunEvent{Type: core.EventRunComplete, RunID: runID, Agent: wf.Name, Result: res})
		return res, failed
	}

	output, err := json.Marshal(state)
	if err != nil {
		output = []byte("{}")
	}
	res := &core.RunResult{
		RunID:      runID,
		Status:     core.RunStatusCompleted,
		Output:     string(output),
		DurationMS: time.Since(started).Milliseconds(),
		Metadata:   map[string]any{"workflow": wf.Name, "steps": len(wf.Steps)},
	}
	emitStep(core.RunEvent{Type: core.EventRunComplete, RunID: runID, Agent: wf.Name, Result: res})
	return res, nil
}

// Agent returns a named agent (nil when absent).
func (c *StepContext) Agent(name string) *agent.Agent {
	return c.wf.Agents[name]
}

// Set stores a value in the shared state.
func (c *StepContext) Set(key string, v any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.State[key] = v
}

// Get reads a value from the shared state.
func (c *StepContext) Get(key string) any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.State[key]
}

// Input returns the workflow input.
func (c *StepContext) Input() any {
	return c.Get("input")
}

// Log emits a custom step event.
func (c *StepContext) Log(format string, args ...any) {
	if c.Emit != nil {
		msg := fmt.Sprintf(format, args...)
		c.Emit(core.RunEvent{Type: core.EventStepEnd, RunID: c.RunID, Agent: c.wf.Name, Step: c.Step, Data: json.RawMessage(fmt.Sprintf(`{"log":%q}`, msg))})
	}
}

// Describe renders the workflow graph as text (for `ernest doctor`).
func (wf *Workflow) Describe() string {
	var sb strings.Builder
	sb.WriteString(wf.Name + "\n")
	for _, s := range wf.Steps {
		sb.WriteString(fmt.Sprintf("  - %s", s.Name))
		if len(s.DependsOn) > 0 {
			sb.WriteString(" (after " + strings.Join(s.DependsOn, ", ") + ")")
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func newID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}
