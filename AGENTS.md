# AGENTS.md — fedlet

Federated messaging bridge: ingests messages from multiple chat/IM protocols and publishes them over a libp2p P2P mesh.

## Structure

```
fedbridge/         — Main Go app (entrypoint: main.go). Build with:
                     cd fedbridge && go build -v -tags gomuks,toxoverhttp,outlookgraph,emailimap
fbprotocols/       — Protocol backend Go packages
  emailimap/       IMAP email polling (most mature)
  irccloud/        IRCCloud integration
  outlookgraph/    Microsoft Graph API
  gomuks/          Matrix via gomuks websocket
  toxoverhttp/     Tox over HTTP REST
  nostr/ misskey/ mailchat/ discordpy/ toxoverclib/  — planned/stubs (mostly empty)
fedpubhttp/        — HTTP-to-P2P publishing utility (Go package + shell script)
curlrq/            — V language module: parallel HTTP via libcurl (c2v bindings)
```

Empty dirs (`cmd/`, `fbtransports/`, `fednet/`, `qlfed/`, `web/`) are future placeholders.

## Build & Run

- **fedbridge** is the primary binary. Build from _within_ `fedbridge/`:
  ```
  cd fedbridge && go build -v -tags gomuks,toxoverhttp,outlookgraph,emailimap
  ```
  Tags are how protocols are selected; omit tags to exclude backends.
  Pre-built binaries: `main.gz` (20MB), `main` (32MB), `main.full` (42MB).

- **V code**: `curlrq/curlrq.v` is a V module using `#flag -lcurl` for libcurl. Test with `v run test_curlrq.v`.

## Architecture quirks

- **Backend registration**: Each protocol in `fedbridge/*.go` uses `//go:build <tag>` + `init()` that appends to `var starters []func()`. `main.go` iterates `starters`. To add a new protocol, create a new file with the build tag guard and append to `starters`.
- **Publish routing**: Messages can go via HTTP POST to `http://127.0.0.1:4004/p2pin/send?topic=...` (default) or directly to libp2p via `p2put.PublishTopic`. Controlled by `publishViaHTTP` in `main.go`.
- **Config files**: IMAP state persists to `~/.config/fedlet/imap-state.json`.
- **Server**: The bridge listens on `:4004` with default HTTP handlers.

## Hard constraints

- **Do NOT import `github.com/microsoftgraph/msgraph-sdk-go`** — it causes Go compiler memory exhaustion and OOM (confirmed in readme.md). The `outlookgraph` protocol avoids this; use it as reference.
- The root `go.mod` has minimal deps (go-imap, gorilla/websocket, x/text). Most heavy deps (libp2p, nostr, etc.) live in `fedbridge/go.mod`.
- `fedbridge/go.mod` uses `replace github.com/envsh/fedlet => ../` for local development.

## Language notes

- Server-side code **must** be Go. V is only for small client-side modules (curlrq) — the readme explicitly forbids V for server code due to build/deploy complexity.

## V code

```
v run test_curlrq.v        # run test
v run curlrq/curlrq.v      # not standalone (module)
```
Requires `libcurl` dev headers (`libcurl4-openssl-dev` or equivalent).
