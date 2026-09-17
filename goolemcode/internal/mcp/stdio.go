package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
)

// stdioTransport lanza el servidor MCP como proceso hijo y habla JSON-RPC por
// stdin/stdout (un mensaje JSON por línea). Asíncrono: readLoop correlaciona.
type stdioTransport struct {
	*rpcMux
	spec  ServerSpec
	cmd   *exec.Cmd
	stdin io.WriteCloser
}

func newStdioTransport(spec ServerSpec) *stdioTransport {
	return &stdioTransport{rpcMux: newRPCMux(), spec: spec}
}

func (t *stdioTransport) start(ctx context.Context) error {
	t.cmd = exec.CommandContext(ctx, t.spec.Command, t.spec.Args...)
	t.cmd.Stderr = os.Stderr // el logging del servidor MCP suele ir por stderr
	stdin, err := t.cmd.StdinPipe()
	if err != nil {
		return err
	}
	t.stdin = stdin
	stdout, err := t.cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := t.cmd.Start(); err != nil {
		return err
	}
	go t.readLoop(stdout)
	return nil
}

func (t *stdioTransport) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var resp rpcResp
		if err := json.Unmarshal(line, &resp); err != nil {
			continue // línea no-JSON (logging) → ignorar
		}
		t.dispatch(resp)
	}
}

func (t *stdioTransport) request(method string, params any) (json.RawMessage, error) {
	id, ch := t.alloc()
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	b, _ := json.Marshal(msg)
	if _, err := t.stdin.Write(append(b, '\n')); err != nil {
		return nil, err
	}
	return t.wait(ch, method)
}

func (t *stdioTransport) notify(method string, params any) error {
	msg := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	b, _ := json.Marshal(msg)
	_, err := t.stdin.Write(append(b, '\n'))
	return err
}

func (t *stdioTransport) stop() {
	if t.cmd != nil && t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
}
