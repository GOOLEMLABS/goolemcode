package provider

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/GOOLEMLABS/goolemcode/internal/debug"
)

// transientStatuses son códigos que suelen ser temporales: reintentar ayuda.
var transientStatuses = map[int]bool{
	http.StatusTooManyRequests:     true, // 429
	http.StatusInternalServerError: true, // 500
	http.StatusBadGateway:          true, // 502
	http.StatusServiceUnavailable:  true, // 503
	http.StatusGatewayTimeout:      true, // 504
}

// doWithRetry envía la petición reintentando SOLO antes del primer byte útil
// (errores de red o estados transitorios), con backoff exponencial. Una vez que
// devuelve 200 y empezamos a leer el stream, no se reintenta (ya emitimos texto).
// El cuerpo se reconstruye en cada intento a partir de body.
func doWithRetry(ctx context.Context, client *http.Client, method, url string, headers map[string]string, body []byte, attempts int) (*http.Response, error) {
	var lastErr error
	backoff := 500 * time.Millisecond
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
		}
		req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			debug.Logf("retry %d/%d %s %s: network error: %v", attempt+1, attempts, method, url, err)
			continue // error de red: reintenta
		}
		if transientStatuses[resp.StatusCode] {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
			debug.Logf("retry %d/%d %s %s: HTTP %d", attempt+1, attempts, method, url, resp.StatusCode)
			continue // estado transitorio: reintenta
		}
		return resp, nil // éxito o error no transitorio (lo maneja el llamante)
	}
	return nil, fmt.Errorf("tras %d intentos: %w", attempts, lastErr)
}
