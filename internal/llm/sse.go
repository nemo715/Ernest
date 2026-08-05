package llm

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// SSEEvent is a parsed Server-Sent Events frame.
type SSEEvent struct {
	Event string
	Data  []byte
	ID    string
}

// ParseSSE reads an SSE stream, calling emit for each complete event.
// Comments (lines starting with ':') are ignored; a blank line terminates
// an event. This is the standard format used by OpenAI, Anthropic and
// Gemini streaming endpoints.
func ParseSSE(r io.Reader, emit func(SSEEvent) error) error {
	br := bufio.NewReader(r)
	var (
		event   string
		data    strings.Builder
		id      string
		inEvent bool
	)
	for {
		line, err := br.ReadString('\n')
		if err != nil && err != io.EOF {
			return err
		}
		trimmed := strings.TrimRight(line, "\r\n")
		switch {
		case trimmed == "":
			if inEvent {
				if err := emit(SSEEvent{Event: event, Data: []byte(strings.TrimSpace(data.String())), ID: id}); err != nil {
					return err
				}
			}
			event, id, inEvent = "", "", false
			data.Reset()
		case strings.HasPrefix(trimmed, ":"):
			// comment — ignore
		default:
			inEvent = true
			if idx := strings.Index(trimmed, ":"); idx >= 0 {
				field, value := trimmed[:idx], strings.TrimPrefix(trimmed[idx+1:], " ")
				switch field {
				case "event":
					event = value
				case "data":
					if data.Len() > 0 {
						data.WriteByte('\n')
					}
					data.WriteString(value)
				case "id":
					id = value
				}
			}
		}
		if err == io.EOF {
			if inEvent {
				if err := emit(SSEEvent{Event: event, Data: []byte(strings.TrimSpace(data.String())), ID: id}); err != nil {
					return err
				}
			}
			return nil
		}
	}
}

// SSEEventSource parses SSE data lines that contain one JSON document per
// data field. [DONE] is skipped.
func SSEEventSource(data []byte, emit func(jsonData []byte) error) error {
	s := strings.TrimSpace(string(data))
	if s == "" || s == "[DONE]" {
		return nil
	}
	if err := emit([]byte(s)); err != nil {
		return err
	}
	return nil
}

// requireField returns a typed error for missing payload fields.
func requireField(obj map[string]any, field string) error {
	return fmt.Errorf("missing field %q in provider payload", field)
}
