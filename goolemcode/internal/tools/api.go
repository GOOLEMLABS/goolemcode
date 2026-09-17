package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/GOOLEMLABS/goolemcode/internal/model"
)

const apiTimeoutDef = 30
const apiMaxBody = 50000

func RegisterAPI(reg *Registry) {
	client := &http.Client{Timeout: time.Duration(apiTimeoutDef) * time.Second}

	reg.Register(model.ToolDefinition{
		Name:        "api_request",
		Description: "Make an HTTP request to a REST API. Supports GET, POST, PUT, PATCH, DELETE. Returns status code, headers, and body.",
		InputSchema: obj(map[string]any{
			"method":          prop("string", "HTTP method: GET, POST, PUT, PATCH, DELETE"),
			"url":             prop("string", "Full URL (include http:// or https://)"),
			"headers":         prop("string", "Headers in JSON format (optional, e.g. {\"Authorization\": \"Bearer xyz\"})"),
			"body":            prop("string", "Request body (optional, only for POST/PUT/PATCH)"),
			"timeout_seconds": prop("integer", fmt.Sprintf("Timeout in seconds (default %d)", apiTimeoutDef)),
		}, "method", "url"),
		Mutating: true,
	}, func(ctx context.Context, args map[string]any) (string, error) {
		method := strings.ToUpper(str(args["method"]))
		url := str(args["url"])
		if method == "" || url == "" {
			return "", fmt.Errorf("method and url are required")
		}

		timeout := apiTimeoutDef
		if v, ok := toInt(args["timeout_seconds"]); ok && v > 0 {
			timeout = v
		}
		reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		defer cancel()

		bodyStr := str(args["body"])
		req, err := http.NewRequestWithContext(reqCtx, method, url, strings.NewReader(bodyStr))
		if err != nil {
			return "", err
		}

		if bodyStr != "" && req.Header.Get("Content-Type") == "" {
			req.Header.Set("Content-Type", "application/json")
		}

		if h := str(args["headers"]); h != "" {
			var headers map[string]string
			if err = json.Unmarshal([]byte(h), &headers); err == nil {
				for k, v := range headers {
					req.Header.Set(k, v)
				}
			}
		}

		resp, err := client.Do(req)
		if err != nil {
			return fmt.Sprintf("Error on %s %s: %s", method, url, err), nil
		}
		defer resp.Body.Close()

		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, apiMaxBody))
		bodyStr = string(bodyBytes)
		if len(bodyStr) > apiMaxBody {
			bodyStr = bodyStr[:apiMaxBody] + "\n… (truncado)"
		}

		var sb strings.Builder
		fmt.Fprintf(&sb, "%s %s → HTTP %d\n\n", method, url, resp.StatusCode)
		for k, vs := range resp.Header {
			for _, v := range vs {
				if len(v) < 120 {
					fmt.Fprintf(&sb, "%s: %s\n", k, v)
				}
			}
		}
		if bodyStr != "" {
			fmt.Fprintf(&sb, "\n%s", bodyStr)
		}
		return sb.String(), nil
	})
}
