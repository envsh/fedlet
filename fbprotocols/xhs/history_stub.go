package xhs

// Browse-history placeholder.
//
// The xhs web API exposes no browse-history endpoint. A full method-level
// re-verification of ReaJason/xhs core.py (2026-09) found only the reporting
// call POST /api/sns/web/v1/note/metrics_report (which pushes view metrics,
// never reads history back); the APP "历史记录" screen is search/shelf history
// and is not served by any public web route. MediaCrawler and the web client
// share the same signed /api/sns/web set without such a method.
//
// Therefore xhs_history is registered as a placeholder kind only: it logs, never
// sends a request, and is deliberately NOT wired into any poll loop.

import (
	"errors"
	"log"
)

// HistoryKindUnsupported is the registered (placeholder) kind string.
const HistoryKindUnsupported = "xhs_history"

// ErrHistoryUnsupported is returned by FetchHistory: the endpoint does not
// exist, so the placeholder signals "unsupported" rather than silently no-op.
var ErrHistoryUnsupported = errors.New("xhs: browse history not supported (no public web endpoint)")

// FetchHistory is the empty implementation of the unroutable kind. It never
// touches the network and is not called by the poll loop; it exists so the
// kind contract is explicit and greppable.
func FetchHistory() error {
	log.Printf("xhs: %v (placeholder kind %q registered, not polled)", ErrHistoryUnsupported, HistoryKindUnsupported)
	return ErrHistoryUnsupported
}
