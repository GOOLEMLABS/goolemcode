package tools

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/GOOLEMLABS/goolemcode/internal/model"
)

const (
	webMaxBytes    = 2 * 1024 * 1024 // download limit
	webMaxCharsDef = 20000           // texto devuelto por defecto
	webSearchDefN  = 8               // default web_search results
)

var (
	reScriptStyle = regexp.MustCompile(`(?is)<(script|style)\b[^>]*>.*?</(script|style)>`)
	reTag         = regexp.MustCompile(`(?s)<[^>]+>`)
	reBlankLines  = regexp.MustCompile(`\n[ \t]*\n[ \t]*\n+`)
	reANSI        = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`) // terminal color sequences
)

// RegisterWeb registers `web_fetch`: downloads a URL (http/https) and returns
// its text (HTML converted to plain text). Read-only → Mutating=false.
func RegisterWeb(reg *Registry) {
	client := &http.Client{Timeout: 30 * time.Second}

	reg.Register(model.ToolDefinition{
		Name:        "web_fetch",
		Description: "Download a URL (http/https) and return its text (HTML is converted to plain text). Useful for reading documentation. Note: accesses any network host, including localhost.",
		InputSchema: obj(map[string]any{
			"url":       prop("string", "URL http(s) to download"),
			"max_chars": prop("integer", fmt.Sprintf("Maximum characters to return (default %d)", webMaxCharsDef)),
		}, "url"),
		Mutating: false,
	}, func(ctx context.Context, args map[string]any) (string, error) {
		raw := strings.TrimSpace(str(args["url"]))
		if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
			return "", fmt.Errorf("URL must start with http:// or https://")
		}
		maxChars := webMaxCharsDef
		if v, ok := toInt(args["max_chars"]); ok && v > 0 {
			maxChars = v
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("User-Agent", "GoolemCode/0.1 (+web_fetch)")
		resp, err := client.Do(req)
		if err != nil {
			return "", fmt.Errorf("could not download: %w", err)
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(io.LimitReader(resp.Body, webMaxBytes))
		ct := resp.Header.Get("Content-Type")
		text := string(body)
		if strings.Contains(ct, "text/html") || strings.Contains(text[:min(len(text), 256)], "<html") {
			text = htmlToText(text)
		}
		text = reANSI.ReplaceAllString(text, "") // limpia colores de terminal (p. ej. wttr.in)
		text = strings.TrimSpace(text)
		truncated := false
		if len(text) > maxChars {
			text = text[:maxChars]
			truncated = true
		}

		header := fmt.Sprintf("GET %s → HTTP %d (%s)\n\n", raw, resp.StatusCode, ct)
		if truncated {
			text += fmt.Sprintf("\n\n… (truncated to %d characters)", maxChars)
		}
		return header + text, nil
	})

	reg.Register(model.ToolDefinition{
		Name:        "web_search",
		Description: "Search the internet and return real web results (title, URL, snippet). Use for facts, documentation, current events, or to discover URLs to read with web_fetch. You have internet access: do NOT say you cannot search.",
		InputSchema: obj(map[string]any{
			"query":       prop("string", "Search terms"),
			"max_results": prop("integer", fmt.Sprintf("Maximum results (default %d)", webSearchDefN)),
		}, "query"),
		Mutating: false,
	}, func(ctx context.Context, args map[string]any) (string, error) {
		q := strings.TrimSpace(str(args["query"]))
		if q == "" {
			return "", fmt.Errorf("empty query")
		}
		maxResults := webSearchDefN
		if v, ok := toInt(args["max_results"]); ok && v > 0 {
			maxResults = v
		}
		results, err := ddgSearch(ctx, client, q, maxResults)
		if err != nil {
			return "", err
		}
		if len(results) == 0 {
			return fmt.Sprintf("No results for %q. Try rephrasing the search or, if you know a relevant URL, read it with web_fetch.", q), nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Results for: %s\n\n", q)
		for i, r := range results {
			fmt.Fprintf(&b, "%d. %s\n   %s\n", i+1, r.title, r.url)
			if r.snippet != "" {
				fmt.Fprintf(&b, "   %s\n", r.snippet)
			}
		}
		return strings.TrimRight(b.String(), "\n"), nil
	})
}

type searchResult struct{ title, url, snippet string }

// ddgSearch queries DuckDuckGo's HTML (JS-free endpoint) via POST with browser
// headers —returning real web results without a headless browser or API key— and
// extracts title, URL, and snippet from each organic result (skips ads).
func ddgSearch(ctx context.Context, client *http.Client, query string, maxResults int) ([]searchResult, error) {
	form := url.Values{"q": {query}, "kl": {"es-es"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://html.duckduckgo.com/html/", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	req.Header.Set("Accept-Language", "es-ES,es;q=0.9,en;q=0.8")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not search: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, webMaxBytes))
	return parseDDGResults(string(body), maxResults), nil
}

var (
	reResultA    = regexp.MustCompile(`(?is)class="result__a"[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
	reResultSnip = regexp.MustCompile(`(?is)class="result__snippet"[^>]*>(.*?)</a>`)
)

// parseDDGResults extracts organic results from DuckDuckGo's HTML.
func parseDDGResults(htmlBody string, maxResults int) []searchResult {
	locs := reResultA.FindAllStringSubmatchIndex(htmlBody, -1)
	var out []searchResult
	for i, loc := range locs {
		rawURL := htmlBody[loc[2]:loc[3]]
		title := stripTags(htmlBody[loc[4]:loc[5]])
		if isDDGAd(rawURL) || title == "" {
			continue
		}
		u := normalizeDDGURL(rawURL)
		// The snippet is between the end of this link and the start of the next one.
		segEnd := len(htmlBody)
		if i+1 < len(locs) {
			segEnd = locs[i+1][0]
		}
		snippet := ""
		if m := reResultSnip.FindStringSubmatch(htmlBody[loc[1]:segEnd]); m != nil {
			snippet = stripTags(m[1])
		}
		out = append(out, searchResult{title: title, url: u, snippet: snippet})
		if len(out) >= maxResults {
			break
		}
	}
	return out
}

func isDDGAd(u string) bool {
	return strings.Contains(u, "/y.js") || strings.Contains(u, "ad_domain=") || strings.Contains(u, "ad_provider=")
}

// normalizeDDGURL resuelve el redirect //duckduckgo.com/l/?uddg=<url> y los
// esquemas relativos al protocolo.
func normalizeDDGURL(u string) string {
	u = html.UnescapeString(u)
	if idx := strings.Index(u, "uddg="); idx >= 0 {
		v := u[idx+len("uddg="):]
		if amp := strings.IndexByte(v, '&'); amp >= 0 {
			v = v[:amp]
		}
		if dec, err := url.QueryUnescape(v); err == nil {
			return dec
		}
	}
	if strings.HasPrefix(u, "//") {
		return "https:" + u
	}
	return u
}

func stripTags(s string) string {
	s = reTag.ReplaceAllString(s, "")
	return strings.TrimSpace(html.UnescapeString(s))
}

func htmlToText(s string) string {
	s = reScriptStyle.ReplaceAllString(s, " ")
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = reBlankLines.ReplaceAllString(s, "\n\n")
	return s
}
