package llm

import (
	"strings"
	"testing"
)

func TestParseSSEBasic(t *testing.T) {
	input := "event: message\n" +
		"data: {\"a\":1}\n" +
		"id: 42\n" +
		"\n" +
		"data: second\n" +
		"\n"
	var got []SSEEvent
	if err := ParseSSE(strings.NewReader(input), func(ev SSEEvent) error {
		got = append(got, ev)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 events, got %d", len(got))
	}
	if got[0].Event != "message" || string(got[0].Data) != `{"a":1}` || got[0].ID != "42" {
		t.Fatalf("event0 wrong: %+v", got[0])
	}
	if string(got[1].Data) != "second" {
		t.Fatalf("event1 wrong: %+v", got[1])
	}
}

func TestParseSSEMultiLineData(t *testing.T) {
	input := "data: line1\n" +
		"data: line2\n" +
		"\n"
	var got []SSEEvent
	if err := ParseSSE(strings.NewReader(input), func(ev SSEEvent) error {
		got = append(got, ev)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 event, got %d", len(got))
	}
	if string(got[0].Data) != "line1\nline2" {
		t.Fatalf("multiline data wrong: %q", got[0].Data)
	}
}

func TestParseSSECommentsAndEOF(t *testing.T) {
	input := ": keepalive comment\n" +
		"data: hello\n" + // no trailing blank line — flushed at EOF
		""
	var got []SSEEvent
	if err := ParseSSE(strings.NewReader(input), func(ev SSEEvent) error {
		got = append(got, ev)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || string(got[0].Data) != "hello" {
		t.Fatalf("EOF flush wrong: %+v", got)
	}
}

func TestParseSSEReturnsEmitError(t *testing.T) {
	boom := "emit failed"
	err := ParseSSE(strings.NewReader("data: x\n\n"), func(ev SSEEvent) error {
		return &parseErr{msg: boom}
	})
	if err == nil || !strings.Contains(err.Error(), boom) {
		t.Fatalf("emit error must propagate, got %v", err)
	}
}

type parseErr struct{ msg string }

func (e *parseErr) Error() string { return e.msg }

func TestSSEEventSource(t *testing.T) {
	var got [][]byte
	emit := func(b []byte) error {
		got = append(got, b)
		return nil
	}
	if err := SSEEventSource([]byte(`{"x":1}`), emit); err != nil {
		t.Fatal(err)
	}
	if err := SSEEventSource([]byte("[DONE]"), emit); err != nil {
		t.Fatal(err)
	}
	if err := SSEEventSource([]byte("  "), emit); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || string(got[0]) != `{"x":1}` {
		t.Fatalf("SSEEventSource wrong: %v", got)
	}
}
