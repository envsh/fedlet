package hongguo

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// fixtureRouterHTML mirrors the real /category/real-drama embed: a small HTML
// page with the _ROUTER_DATA script wrapping a loaderData blob whose
// category_$ loader holds recommendList with two verbatim drama items.
var fixtureRouterHTML = `<!doctype html><html><head>
<script>window.__CONF={"x":1}</script>
</head><body><div id="app">skeleton…</div>
<script>
_ROUTER_DATA = {"loaderData":{"category_$":{"recommendList":[
{"series_id":7111943588446997534,"series_name":"天神殿","series_cover":"https://p3.byteimg.com/cover/1.jpg","series_intro":"曾被夺走一切,如今……","tags":["复仇","都市日常"],"episode_cnt":217},
{"series_id":7683196130645003288,"series_name":"东莞市云,从野草到商业女王","series_cover":"https://p3.byteimg.com/cover/2.jpg","series_intro":"……","tags":["年代","逆袭"],"episode_cnt":80}
]}},"errors":{}}</script>
</body></html>`

// fixtureRouterNoList is the same page shape but with an empty recommendList.
var fixtureRouterNoList = `<!doctype html><html><body><script>
_ROUTER_DATA = {"loaderData":{"category_$":{"recommendList":[]}},"errors":{}}</script>
</body></html>`

// fixtureRouterHTMLNoise lacks the script but has a recommended word elsewhere,
// to prove extraction explicitly fails (marker must be present).
var fixtureRouterHTMLNoise = `<!doctype html><html><body>
<p>recommendList appears only in normal text, not the embed</p>
</body></html>`

func TestFetchHotBoardRouterExtract(t *testing.T) {
	router, err := extractRouterData([]byte(fixtureRouterHTML))
	if err != nil {
		t.Fatalf("extractRouterData: %v", err)
	}
	resp, err := parseHotBoardItems(router)
	if err != nil {
		t.Fatalf("parseHotBoardItems: %v", err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(resp.Items))
	}
	first := resp.Items[0]
	if first.Rank != 1 {
		t.Errorf("rank = %d, want 1", first.Rank)
	}
	if first.SeriesID != "7111943588446997534" {
		t.Errorf("series_id = %q", first.SeriesID)
	}
	if first.Title != "天神殿" {
		t.Errorf("title = %q", first.Title)
	}
	if first.URL != "https://hongguoduanju.com/detail?series_id=7111943588446997534" {
		t.Errorf("url = %q", first.URL)
	}
	if first.EpisodeCnt != 217 {
		t.Errorf("episode_cnt = %d, want 217", first.EpisodeCnt)
	}
	if len(first.Tags) != 2 || first.Tags[0] != "复仇" {
		t.Errorf("tags = %v", first.Tags)
	}
	// the raw source map must survive for verbatim forwarding
	if _, ok := first.Raw["series_id"]; !ok {
		t.Error("Raw not surfaced")
	}
}

func TestParseHotBoardItemsEmptyIsError(t *testing.T) {
	router, err := extractRouterData([]byte(fixtureRouterNoList))
	if err != nil {
		t.Fatalf("extractRouterData: %v", err)
	}
	if _, err := parseHotBoardItems(router); err == nil {
		t.Fatal("want error for empty recommendList")
	}
}

func TestExtractRouterDataMissingIsError(t *testing.T) {
	if _, err := extractRouterData([]byte(fixtureRouterHTMLNoise)); err == nil {
		t.Fatal("want error for missing _ROUTER_DATA embed")
	} else if !strings.Contains(err.Error(), "_ROUTER_DATA") {
		t.Errorf("error should mention the embed marker, got %v", err)
	}
}

func TestExtractRouterDataRejectLiteralNoise(t *testing.T) {
	// HTML containing a fake marker whose body is not balanced JSON (site
	// layout change / truncated chunk) must be a hard error, not a panic.
	body := []byte(`<script>_ROUTER_DATA = {"loaderData":{"category_$":{"recommendList":[</script>`)
	if _, err := extractRouterData(body); err == nil {
		t.Fatal("want error for truncated _ROUTER_DATA embed")
	}
}

func TestFetchHotBoardRouterKeysShape(t *testing.T) {
	// Ensure the parsed router map walks tolerate a renamed category loader key
	// (the site may switch category_$ to something else).
	variants := []string{
		`{"loaderData":{"category_$":{"recommendList":[{"series_id":1,"series_name":"a"}]}},"errors":{}}`,
		`{"loaderData":{"category_x":{"recommendList":[{"series_id":2,"series_name":"b"}]}},"errors":{}}`,
		`{"loaderData":{"other":{"nested":{"recommendList":[{"series_id":3,"series_name":"c"}]}}},"errors":{}}`,
	}
	for i, v := range variants {
		body := []byte(`<script>_ROUTER_DATA = ` + v + `</script>`)
		router, err := extractRouterData(body)
		if err != nil {
			t.Fatalf("variant %d extract: %v", i, err)
		}
		resp, err := parseHotBoardItems(router)
		if err != nil {
			t.Fatalf("variant %d parse: %v", i, err)
		}
		if len(resp.Items) != 1 {
			t.Fatalf("variant %d items = %d", i, len(resp.Items))
		}
	}
}

// TestHotBoardRespJSONShape pins the published envelope so a rename does not
// silently change what callers (fedbridge) see.
func TestHotBoardRespJSONShape(t *testing.T) {
	resp := &HotBoardResp{
		Items: []HotBoardItem{
			{Rank: 1, SeriesID: "7111943588446997534", Title: "天神殿",
				URL: "https://hongguoduanju.com/detail?series_id=7111943588446997534",
				Cover: "https://p3.byteimg.com/cover/1.jpg", Tags: []string{"复仇"},
				EpisodeCnt: 217},
		},
	}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	for _, want := range []string{`"list"`, `"title":"天神殿"`, `"series_id":"7111943588446997534"`} {
		if !strings.Contains(got, want) {
			t.Errorf("envelope missing %s (body %s)", want, got)
		}
	}
	// the flat publish path must marshal verbatim; confirm no divergence from
	// the source shape by decoding twice.
	var back HotBoardResp
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal round trip: %v", err)
	}
	if back.Items[0].SeriesID != resp.Items[0].SeriesID {
		t.Errorf("round trip series_id mismatch")
	}
}

// TestExtractRouterDataWindowPrefixTolerated documents that a source page
// carrying the legacy "window." prefix still parses: the unprefixed marker is
// a substring of it, and the page's JS references are walked for the first
// decodable object. The actual drift guard is TestRouterMarkerStaysUnprefixed.
func TestExtractRouterDataWindowPrefixTolerated(t *testing.T) {
	body := []byte(`<script>window._ROUTER_DATA = {"loaderData":{"category_$":{"recommendList":[{"series_id":7}]}},"errors":{}}</script>`)
	router, err := extractRouterData(body)
	if err != nil {
		t.Fatalf("window.-prefixed source must still extract: %v", err)
	}
	resp, err := parseHotBoardItems(router)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(resp.Items) != 1 || resp.Items[0].SeriesID != "7" {
		t.Fatalf("items = %+v", resp.Items)
	}
}

// TestExtractRouterDataModernNodeFallback exercises the __MODERN_ROUTER_DATA__
// JSON-node fallback path.
func TestExtractRouterDataModernNodeFallback(t *testing.T) {
	body := []byte(`<!doctype html><script>
	window.initRouterData = function(){};
	</script><script id="__MODERN_ROUTER_DATA__" type="application/json">
	{"loaderData":{"category_$":{"recommendList":[{"series_id":42}]}},"errors":{}}
	</script></body></html>`)
	router, err := extractRouterData(body)
	if err != nil {
		t.Fatalf("fallback node: %v", err)
	}
	resp, err := parseHotBoardItems(router)
	if err != nil {
		t.Fatalf("parse fallback: %v", err)
	}
	if len(resp.Items) != 1 || resp.Items[0].SeriesID != "42" {
		t.Fatalf("fallback items = %+v", resp.Items)
	}
}

// TestRouterMarkerStaysUnprefixed is a guard so the marker constant cannot be
// silently reverted to the window.-prefixed literal that broke live polling.
func TestRouterMarkerStaysUnprefixed(t *testing.T) {
	if bytes.Contains([]byte(routerMarker), []byte("window.")) {
		t.Fatal("routerMarker must not carry a window. prefix")
	}
}
