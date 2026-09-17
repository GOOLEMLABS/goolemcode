package tools

import "testing"

// Fixture with the real DuckDuckGo HTML structure: one ad (y.js), one direct
// result with snippet, and one result with uddg redirect without snippet.
const ddgFixture = `
<div class="result result--ad">
  <a class="result__a" href="https://duckduckgo.com/y.js?ad_domain=booking.com&amp;ad_provider=bingv7aa">Hoteles baratos</a>
  <a class="result__snippet" href="https://duckduckgo.com/y.js?x">Reserva ya</a>
</div>
<div class="result">
  <a class="result__a" href="https://go.dev/">The Go <b>Programming</b> Language</a>
  <a class="result__snippet" href="https://go.dev/">Build simple, secure, scalable systems with Go.</a>
</div>
<div class="result">
  <a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fes.wikipedia.org%2Fwiki%2FGo&amp;rut=abc">Go - Wikipedia</a>
</div>`

func TestParseDDGResultsFiltersAdsAndDecodes(t *testing.T) {
	res := parseDDGResults(ddgFixture, 10)
	if len(res) != 2 {
		t.Fatalf("expected 2 organic results (ad filtered), got %d: %+v", len(res), res)
	}

	// First organic: direct URL, title without tags, snippet present.
	if res[0].url != "https://go.dev/" {
		t.Errorf("url[0] = %q", res[0].url)
	}
	if res[0].title != "The Go Programming Language" {
		t.Errorf("title[0] = %q (deben quitarse las <b>)", res[0].title)
	}
	if res[0].snippet != "Build simple, secure, scalable systems with Go." {
		t.Errorf("snippet[0] = %q", res[0].snippet)
	}

	// Second: the uddg redirect must be decoded to the real URL.
	if res[1].url != "https://es.wikipedia.org/wiki/Go" {
		t.Errorf("url[1] = %q (uddg no decodificado)", res[1].url)
	}
	if res[1].snippet != "" {
		t.Errorf("snippet[1] should be empty, got %q (did it take the ad's snippet?)", res[1].snippet)
	}
}

func TestParseDDGResultsRespectsMax(t *testing.T) {
	if res := parseDDGResults(ddgFixture, 1); len(res) != 1 {
		t.Fatalf("max_results=1 should give 1, got %d", len(res))
	}
}

func TestParseDDGResultsEmpty(t *testing.T) {
	if res := parseDDGResults("<html><body>nada</body></html>", 10); len(res) != 0 {
		t.Fatalf("no results should give 0, got %d", len(res))
	}
}
