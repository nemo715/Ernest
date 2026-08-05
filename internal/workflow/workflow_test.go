package workflow

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nemo715/Ernest/internal/core"
)

func TestLinearDAG(t *testing.T) {
	var order []string
	var mu sync.Mutex
	record := func(s string) func(ctx *StepContext) error {
		return func(ctx *StepContext) error {
			mu.Lock()
			order = append(order, s)
			mu.Unlock()
			return nil
		}
	}
	wf := New("linear",
		&Step{Name: "a", Run: record("a")},
		&Step{Name: "b", DependsOn: []string{"a"}, Run: record("b")},
		&Step{Name: "c", DependsOn: []string{"b"}, Run: record("c")},
	)
	res, err := wf.Run(context.Background(), "in")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != core.RunStatusCompleted {
		t.Fatalf("status = %s", res.Status)
	}
	if strings.Join(order, ",") != "a,b,c" {
		t.Fatalf("order = %v", order)
	}
	if res.Metadata["workflow"] != "linear" {
		t.Fatalf("metadata = %+v", res.Metadata)
	}
}

func TestParallelBranches(t *testing.T) {
	start := make(chan struct{})
	var mu sync.Mutex
	entered := 0
	block := func() func(ctx *StepContext) error {
		return func(ctx *StepContext) error {
			mu.Lock()
			entered++
			mu.Unlock()
			<-start // wait for the gate
			return nil
		}
	}
	wf := New("par",
		&Step{Name: "p1", Run: block()},
		&Step{Name: "p2", Run: block()},
		&Step{Name: "join", DependsOn: []string{"p1", "p2"}, Run: func(ctx *StepContext) error { return nil }},
	)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := wf.Run(ctx, nil)
		done <- err
	}()
	// Both branches must have entered before we release the gate: a
	// serial scheduler would deadlock here (join waits for p2 which
	// waits for the gate).
	for {
		mu.Lock()
		n := entered
		mu.Unlock()
		if n >= 2 {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("workflow finished early: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	close(start)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("workflow did not finish")
	}
}

func TestGuardSkips(t *testing.T) {
	ran := false
	wf := New("guards",
		&Step{
			Name: "skip",
			Guard: func(ctx *StepContext) (bool, error) { return false, nil },
			Run:  func(ctx *StepContext) error { ran = true; return nil },
		},
		&Step{Name: "run", DependsOn: []string{"skip"}, Run: func(ctx *StepContext) error { return nil }},
	)
	res, err := wf.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != core.RunStatusCompleted {
		t.Fatalf("status = %s", res.Status)
	}
	if ran {
		t.Fatal("guarded step must not run")
	}
}

func TestGuardErrorFails(t *testing.T) {
	wf := New("guard-err",
		&Step{Name: "g", Guard: func(ctx *StepContext) (bool, error) { return false, errors.New("guard boom") }, Run: func(ctx *StepContext) error { return nil }},
	)
	wf.MaxRetries = 0
	_, err := wf.Run(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "guard boom") {
		t.Fatalf("err = %v", err)
	}
}

func TestStateSharingAndInput(t *testing.T) {
	wf := New("state",
		&Step{Name: "produce", Run: func(ctx *StepContext) error {
			ctx.Set("x", 42)
			if ctx.Input() != "in" {
				return errors.New("input missing")
			}
			return nil
		}},
		&Step{Name: "consume", DependsOn: []string{"produce"}, Run: func(ctx *StepContext) error {
			if ctx.Get("x") != 42 {
				return errors.New("state not shared")
			}
			return nil
		}},
	)
	_, err := wf.Run(context.Background(), "in")
	if err != nil {
		t.Fatal(err)
	}
}

func TestInitialState(t *testing.T) {
	wf := New("init")
	wf.InitialState = map[string]any{"mode": "test"}
	wf.Steps = []*Step{{
		Name: "check",
		Run: func(ctx *StepContext) error {
			if ctx.Get("mode") != "test" {
				return errors.New("initial state missing")
			}
			return nil
		},
	}}
	_, err := wf.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRetry(t *testing.T) {
	attempts := 0
	wf := New("retry")
	wf.MaxRetries = 3
	wf.Steps = []*Step{{
		Name: "flaky",
		Run: func(ctx *StepContext) error {
			attempts++
			if attempts < 3 {
				return core.NewError(core.KindProvider, "transient")
			}
			return nil
		},
	}}
	res, err := wf.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != core.RunStatusCompleted || attempts != 3 {
		t.Fatalf("status=%s attempts=%d", res.Status, attempts)
	}
}

func TestNonRetryableFailsFast(t *testing.T) {
	attempts := 0
	wf := New("no-retry")
	wf.MaxRetries = 5
	wf.Steps = []*Step{{
		Name: "boom",
		Run: func(ctx *StepContext) error {
			attempts++
			return errors.New("boom") // not a provider/timeout error
		},
	}}
	_, err := wf.Run(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestFailureAbortsDependents(t *testing.T) {
	wf := New("fail")
	wf.MaxRetries = 0
	neverRan := false
	wf.Steps = []*Step{
		{Name: "boom", Run: func(ctx *StepContext) error { return errors.New("boom") }},
		{Name: "never", DependsOn: []string{"boom"}, Run: func(ctx *StepContext) error { neverRan = true; return nil }},
	}
	_, err := wf.Run(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	if neverRan {
		t.Fatal("dependent step ran after failure")
	}
}

func TestDeadlockDetection(t *testing.T) {
	wf := New("cycle",
		&Step{Name: "a", DependsOn: []string{"b"}, Run: func(ctx *StepContext) error { return nil }},
		&Step{Name: "b", DependsOn: []string{"a"}, Run: func(ctx *StepContext) error { return nil }},
	)
	_, err := wf.Run(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "deadlock") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidation(t *testing.T) {
	if _, err := New("empty").Run(context.Background(), nil); err == nil {
		t.Fatal("empty workflow must error")
	}
	wf := New("dup",
		&Step{Name: "a", Run: func(ctx *StepContext) error { return nil }},
		&Step{Name: "a", Run: func(ctx *StepContext) error { return nil }},
	)
	if _, err := wf.Run(context.Background(), nil); err == nil {
		t.Fatal("duplicate names must error")
	}
	wf = New("bad-dep",
		&Step{Name: "a", DependsOn: []string{"ghost"}, Run: func(ctx *StepContext) error { return nil }},
	)
	if _, err := wf.Run(context.Background(), nil); err == nil {
		t.Fatal("unknown dependency must error")
	}
}

func TestStepTimeout(t *testing.T) {
	wf := New("timeout")
	wf.MaxRetries = 0
	wf.Steps = []*Step{{
		Name:    "slow",
		Timeout: 30 * time.Millisecond,
		Run: func(ctx *StepContext) error {
			<-ctx.Ctx.Done()
			return ctx.Ctx.Err()
		},
	}}
	_, err := wf.Run(context.Background(), nil)
	if err == nil {
		t.Fatal("timeout must fail the step")
	}
}

func TestDescribe(t *testing.T) {
	wf := New("describe",
		&Step{Name: "a", Run: func(ctx *StepContext) error { return nil }},
		&Step{Name: "b", DependsOn: []string{"a"}, Run: func(ctx *StepContext) error { return nil }},
	)
	out := wf.Describe()
	if !strings.Contains(out, "describe") || !strings.Contains(out, "after a") {
		t.Fatalf("describe = %q", out)
	}
}

func TestStreamEvents(t *testing.T) {
	wf := New("evt",
		&Step{Name: "a", Run: func(ctx *StepContext) error { return nil }},
	)
	ch, err := wf.Stream(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var types []core.EventType
	for ev := range ch {
		types = append(types, ev.Type)
	}
	if len(types) == 0 || types[0] != core.EventRunStart || types[len(types)-1] != core.EventRunComplete {
		t.Fatalf("events = %v", types)
	}
	hasStepStart, hasStepEnd := false, false
	for _, ty := range types {
		if ty == core.EventStepStart {
			hasStepStart = true
		}
		if ty == core.EventStepEnd {
			hasStepEnd = true
		}
	}
	if !hasStepStart || !hasStepEnd {
		t.Fatalf("step events missing: %v", types)
	}
}

func TestStepLog(t *testing.T) {
	var got string
	wf := New("log",
		&Step{Name: "a", Run: func(ctx *StepContext) error {
			ctx.Log("hello %s", "world")
			return nil
		}},
	)
	ch, err := wf.Stream(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for ev := range ch {
		if ev.Type == core.EventStepEnd && ev.Step == "a" && strings.Contains(string(ev.Data), "hello world") {
			got = string(ev.Data)
		}
	}
	if !strings.Contains(got, "hello world") {
		t.Fatalf("log event missing: %q", got)
	}
}

func TestWorkflowOutputState(t *testing.T) {
	wf := New("out")
	wf.Steps = []*Step{{
		Name: "a",
		Run: func(ctx *StepContext) error {
			ctx.Set("answer", 42)
			return nil
		},
	}}
	res, err := wf.Run(context.Background(), "q")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, `"answer":42`) || !strings.Contains(res.Output, `"input":"q"`) {
		t.Fatalf("output = %s", res.Output)
	}
}
