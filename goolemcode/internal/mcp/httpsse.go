package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ---------- SSE legacy (2024-11-05): GET /sse + POST al endpoint anunciado ----------

type sseTransport struct {
	*rpcMux
	spec    ServerSpec
	client  *http.Client
	cancel  context.CancelFunc
	postURL string
	ready   chan struct{}
}

func newSSETransport(spec ServerSpec) *sseTransport {
	return &sseTransport{rpcMux: newRPCMux(), spec: spec, client: &http.Client{}, ready: make(chan struct{})}
}

func (t *sseTransport) start(ctx context.Context) error {
	cctx, cancel := context.WithCancel(ctx)
	t.cancel = cancel

	req, err := http.NewRequestWithContext(cctx, http.MethodGet, t.spec.URL, nil)
	if err != nil {
		cancel()
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range t.spec.Headers {
		req.Header.Set(k, v)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		cancel()
		return err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		return fmt.Errorf("SSE %s: HTTP %d", t.spec.URL, resp.StatusCode)
	}
	go t.readSSE(resp.Body)

	select {
	case <-t.ready:
		return nil
	case <-time.After(15 * time.Second):
		cancel()
		return fmt.Errorf("SSE %s: no se recibió el evento endpoint", t.spec.URL)
	}
}

func (t *sseTransport) readSSE(body io.ReadCloser) {
	defer body.Close()
	base, _ := url.Parse(t.spec.URL)
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	event, data := "", strings.Builder{}
	flush := func() {
		defer func() { event = ""; data.Reset() }()
		payload := data.String()
		switch event {
		case "endpoint":
			ref, err := url.Parse(strings.TrimSpace(payload))
			if err != nil {
				return
			}
			t.postURL = base.ResolveReference(ref).String()
			select {
			case <-t.ready: // ya cerrado
			default:
				close(t.ready)
			}
		default: // "message" o sin nombre → respuesta JSON-RPC
			if strings.TrimSpace(payload) == "" {
				return
			}
			var resp rpcResp
			if json.Unmarshal([]byte(payload), &resp) == nil {
				t.dispatch(resp)
			}
		}
	}

	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, ":"):
			// comentario/keep-alive → ignorar
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[len("event:"):])
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(line[len("data:"):]))
		}
	}
}

func (t *sseTransport) post(body []byte) error {
	req, err := http.NewRequest(http.MethodPost, t.postURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range t.spec.Headers {
		req.Header.Set(k, v)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("POST %s: HTTP %d", t.postURL, resp.StatusCode)
	}
	return nil
}

func (t *sseTransport) request(method string, params any) (json.RawMessage, error) {
	id, ch := t.alloc()
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	b, _ := json.Marshal(msg)
	if err := t.post(b); err != nil {
		return nil, err
	}
	return t.wait(ch, method) // la respuesta llega por el stream SSE
}

func (t *sseTransport) notify(method string, params any) error {
	msg := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	b, _ := json.Marshal(msg)
	return t.post(b)
}

func (t *sseTransport) stop() {
	if t.cancel != nil {
		t.cancel()
	}
}

// ---------- Streamable HTTP (2025-03-26): POST único; respuesta JSON o SSE ----------

type httpTransport struct {
	spec      ServerSpec
	client    *http.Client
	mu        sync.Mutex
	nextID    int
	sessionID string
}

func newHTTPTransport(spec ServerSpec) *httpTransport {
	return &httpTransport{spec: spec, client: &http.Client{Timeout: requestTimeout}}
}

func (t *httpTransport) start(_ context.Context) error { return nil } // sin conexión persistente

func (t *httpTransport) send(msg map[string]any, wantID int, expectResp bool) (json.RawMessage, error) {
	b, _ := json.Marshal(msg)
	req, err := http.NewRequest(http.MethodPost, t.spec.URL, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range t.spec.Headers {
		req.Header.Set(k, v)
	}
	t.mu.Lock()
	sid := t.sessionID
	t.mu.Unlock()
	if sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Capturar el id de sesión (initialize lo entrega aquí) antes de nada más.
	if s := resp.Header.Get("Mcp-Session-Id"); s != "" {
		t.mu.Lock()
		t.sessionID = s
		t.mu.Unlock()
	}
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("POST %s: HTTP %d: %s", t.spec.URL, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if !expectResp {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil, nil
	}

	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		return readStreamForID(resp.Body, wantID)
	}
	// application/json: una sola respuesta JSON-RPC.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return decodeRPC(body, wantID)
}

// readStreamForID lee un cuerpo SSE y devuelve el resultado del mensaje cuyo id
// coincide (el servidor puede emitir notificaciones antes de la respuesta).
func readStreamForID(body io.Reader, wantID int) (json.RawMessage, error) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	var data strings.Builder
	flush := func() (json.RawMessage, bool, error) {
		defer data.Reset()
		if strings.TrimSpace(data.String()) == "" {
			return nil, false, nil
		}
		raw, err := decodeRPC([]byte(data.String()), wantID)
		if err != nil {
			return nil, false, err
		}
		if raw == nil {
			return nil, false, nil // no era nuestro id (notificación) → seguir
		}
		return raw, true, nil
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if raw, done, err := flush(); done || err != nil {
				return raw, err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(line[len("data:"):]))
		}
	}
	if raw, done, err := flush(); done || err != nil {
		return raw, err
	}
	return nil, fmt.Errorf("stream sin respuesta para id %d", wantID)
}

// decodeRPC interpreta un mensaje JSON-RPC; devuelve (nil,nil) si el id no
// coincide (era una notificación u otra respuesta).
func decodeRPC(b []byte, wantID int) (json.RawMessage, error) {
	var resp rpcResp
	if err := json.Unmarshal(b, &resp); err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("mcp: %s", resp.Error.Message)
	}
	if resp.ID != wantID {
		return nil, nil
	}
	if resp.Result == nil {
		return json.RawMessage("{}"), nil
	}
	return resp.Result, nil
}

func (t *httpTransport) request(method string, params any) (json.RawMessage, error) {
	t.mu.Lock()
	t.nextID++
	id := t.nextID
	t.mu.Unlock()
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	return t.send(msg, id, true)
}

func (t *httpTransport) notify(method string, params any) error {
	msg := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	_, err := t.send(msg, 0, false)
	return err
}

func (t *httpTransport) stop() {}
