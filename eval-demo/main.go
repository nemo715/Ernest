package main

import (
	"context"
	"fmt"

	"github.com/nemo715/Ernest/internal/agent"
	"github.com/nemo715/Ernest/internal/core"
	"github.com/nemo715/Ernest/internal/llm"
)

// A minimal programmatic agent. Swap the mock provider for a real one:
//
//	p := llm.OpenAI(os.Getenv("OPENAI_API_KEY"), "gpt-4o-mini")
func main() {
	p := llm.NewMock(llm.MockConfig{})
	a := agent.New("assistant", p)
	a.Instructions = "You are a helpful assistant."
	a.Tools = core.BuiltinTools
	res, err := a.Chat(context.Background(), "Say hello")
	if err != nil {
		panic(err)
	}
	fmt.Println(res.Output)
}
