package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"sync"
	"time"

	"ernest/internal/core"
)

// stdioTransport runs an MCP server as a child process and exchanges
// newline-delimited JSON-RPC over stdin/stdout. Requests are serialised
// by the single stdout reader.
type stdioTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	sc     *bufio.Scanner
	errCh  chan error // receives cmd.Wait() once
	mu     sync.Mutex
	closed bool
}

func newStdioTransport(command string, args []string, extraEnv []string) (*stdioTransport, error) {
	cmd := exec.Command(command, args...)
	if len(extraEnv) > 0 {
		cmd.Env = append(cmd.Environ(), extraEnv...)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, core.NewError(core.KindMCP, "mcp: stdin pipe: "+err.Error(), err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, core.NewError(core.KindMCP, "mcp: stdout pipe: "+err.Error(), err)
	}
	// Server logs must never corrupt our JSON stdout channel.
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, core.NewError(core.KindMCP, "mcp: start "+command+": "+err.Error(), err)
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
	tr := &stdioTransport{cmd: cmd, stdin: stdin, sc: sc, errCh: make(chan error, 1)}
	go func() { tr.errCh <- cmd.Wait() }()
	return tr, nil
}

func (t *stdioTransport) request(ctx context.Context, id int, method string, params any) (json.RawMessage, error) {
	if err := t.write(marshalRPC(id, method, params)); err != nil {
		return nil, err
	}
	for {
		line, err := t.nextLine(ctx)
		if err != nil {
			return nil, err
		}
		var rpc rpcResponse
		if err := json.Unmarshal(line, &rpc); err != nil {
			return nil, core.NewError(core.KindMCP, "mcp: invalid json from server: "+err.Error(), err)
		}
		if !idMatches(rpc.ID, id) {
			continue // server notifications (no id or other id)
		}
		if rpc.Error != nil {
			return nil, rpcResultError(rpc.Error)
		}
		return rpc.Result, nil
	}
}

func (t *stdioTransport) notify(ctx context.Context, method string, params any) error {
	return t.write(marshalNotification(method, params))
}

// idMatches compares a decoded JSON id (any) with our int id.
func idMatches(decoded any, id int) bool {
	f, ok := decoded.(float64)
	if !ok {
		return false
	}
	return int(f) == id
}

func (t *stdioTransport) write(data []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return core.NewError(core.KindMCP, "mcp: server process is not running")
	}
	if _, err := t.stdin.Write(append(data, '\n')); err != nil {
		return core.NewError(core.KindMCP, "mcp: write to server: "+err.Error(), err)
	}
	return nil
}

// nextLine reads one JSON line, honouring context cancellation.
func (t *stdioTransport) nextLine(ctx context.Context) ([]byte, error) {
	type line struct {
		data []byte
		err  error
	}
	ch := make(chan line, 1)
	go func() {
		if t.sc.Scan() {
			ch <- line{data: append([]byte(nil), t.sc.Bytes()...)}
			return
		}
		err := t.sc.Err()
		if err == nil {
			err = io.EOF
		}
		ch <- line{err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, core.NewError(core.KindTimeout, "mcp: "+ctx.Err().Error(), ctx.Err())
	case l := <-ch:
		if l.err != nil {
			return nil, core.NewError(core.KindMCP, "mcp: server closed: "+l.err.Error(), l.err)
		}
		return l.data, nil
	}
}

func (t *stdioTransport) close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	t.mu.Unlock()
	_ = t.stdin.Close() // EOF tells the server we are done
	select {
	case <-t.errCh:
	case <-time.After(2 * time.Second):
		_ = t.cmd.Process.Kill()
		<-t.errCh
	}
	return nil
}
