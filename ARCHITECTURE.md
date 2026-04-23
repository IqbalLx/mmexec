# Architecture

## Overview

mmexec is an HTTP proxy that routes requests between Anthropic and MiniMax based on trigger keywords in the last message's content. It acts as a transparent middleware — the client sends a request as if to Anthropic; mmexec inspects the payload, rewrites it if needed, and forwards to the appropriate upstream.

```
Client  →  mmexec  →  Anthropic  (no trigger, sticky routing off)
Client  →  mmexec  →  MiniMax    (mmexec trigger or sticky routing on)
Client  →  mmexec  →  Anthropic  (mmrelease trigger or sticky routing off)
```

## Request Flow

```
http.Handler
  │
  ├── io.ReadAll(r.Body)           // read full body (always buffered)
  ├── json.Unmarshal → map[string]json.RawMessage
  ├── inspect(raw)                 // routing + trigger detection
  │     ├── "mmrelease" → useMinimax = false, clean trigger, return Anthropic
  │     ├── "mmexec"     → useMinimax = true,  clean trigger, return MiniMax
  │     ├── useMinimax   → return MiniMax
  │     └── otherwise    → return Anthropic
  │
  ├── routing decision logged
  ├── DEBUG=2? dumpRequest()
  │
  ├── MiniMax path:
  │     forward() → read full response → processMiniMaxResponse()
  │               → write response to client unchanged
  │
  └── Anthropic path:
        convertThinkingToUserMessage()  // convert MiniMax thinking blocks
        forward() → stream response to client unchanged
```

## Routing State

`useMinimax` is a global boolean that implements **sticky routing**: once `mmexec` is triggered, all subsequent requests route to MiniMax until `mmrelease` is detected.

```go
var useMinimax bool  // false = Anthropic, true = MiniMax
```

## Thinking Block Conversion

MiniMax returns thinking blocks with Anthropic API shape (`type: "thinking"`, `thinking` field, `signature` field), but the signatures are self-issued content hashes, not Anthropic-issued cryptographic signatures. When Claude Code persists these blocks and later sends them to Anthropic, Anthropic rejects them with signature validation errors.

### Detection

Detection uses the signature field as the primary signal:

```go
SHA256(TrimSpace(thinking_content)) == signature
```

If the computed SHA256 of the thinking content matches the signature field, the block originated from MiniMax (MiniMax uses `SHA256(content)` as the signature, not a cryptographic HMAC).

A secondary fallback uses hash files at `~/.claude/mmexec/thinking/<hex_hash>` (empty marker files written when MiniMax responses are received). This is only consulted if `thinkStoreBase` is set and the primary signature check fails.

### Conversion

For each detected MiniMax thinking block:
1. Remove the thinking block from the assistant message's content array
2. Append a user message: `{"type": "text", "content": "previous assistant thought process: <thinking>"}`
3. Delete the hash file (one-time use)

No other payload fields are modified (tools, metadata, system prompts, etc.).

## Functions

### `inspect(raw map[string]json.RawMessage) (target string, out map[string]json.RawMessage)`

Reads the `messages` array, examines the last message's content for triggers, and returns the routing target (`"minimax"` or `"anthropic"`) plus the potentially rewritten request body (trigger keywords stripped, model field set for MiniMax).

### `detectTrigger(contentRaw json.RawMessage) string`

Scans the last message content for `mmexec` or `mmrelease` prefixes. Handles both plain-string content and array-of-blocks content.

### `cleanTrigger(last map[string]json.RawMessage, contentRaw json.RawMessage, triggerToStrip string)`

Strips the trigger prefix from the last message content. Mutates `last["content"]` in place.

### `convertThinkingToUserMessage(body []byte) []byte`

Scans all assistant messages for MiniMax thinking blocks. Detection: `SHA256(TrimSpace(thinking)) == signature` (primary) or hash file exists (fallback). Matching blocks are removed and replaced with user messages. Returns the modified JSON body.

### `processMiniMaxResponse(body []byte) error`

Parses a MiniMax API response JSON, extracts all `type: "thinking"` blocks, and writes empty marker files to `~/.claude/mmexec/thinking/<sha256_hex>` for each block. Non-JSON responses (SSE, error pages) are silently skipped. Errors are logged but do not block forwarding.

### `forward(w http.ResponseWriter, r *http.Request, body []byte, baseURL, apiKey, anthropicVersion string)`

Constructs an upstream HTTP request, copies relevant headers, sets auth headers, and forwards the body. MiniMax path: reads full response body → `processMiniMaxResponse` → writes to client. Anthropic path: streams response directly via `io.Copy`.

## Think Store

```
~/.claude/mmexec/thinking/
  ├── <sha256_hex_1>   // empty marker files
  ├── <sha256_hex_2>
  └── ...
```

- Created by `initThinkStore()` at startup
- `thinkStoreBase = ""` disables all store operations (silent no-op)
- Hash files are one-time use: consumed during `convertThinkingToUserMessage`
- SHA256 is computed over `strings.TrimSpace(block.Thinking)` to handle whitespace normalization

## Header Routing

| Header | Routing |
|---|---|
| `Content-Type: application/json` | Always forwarded |
| `anthropic-version` | Only to Anthropic; stripped for MiniMax |
| `Authorization` / `x-api-key` | Only to MiniMax (set from env) |
| `Host`, `X-Forwarded-*`, `Via`, `Forwarded` | Skipped (set by proxies) |
| All other headers | Copied from incoming request |

## Environment Variables

| Variable | Required | Purpose |
|---|---|---|
| `MINIMAX_API_KEY` | Yes | API key for MiniMax upstream |
| `PORT` | No | Listen port, defaults to `9099` |
| `DEBUG` | No | `1` = console, `2` = console + file dumps |

## Debug Artifacts

`DEBUG=2` writes truncated request bodies to `logs/<unix_nano>-<label>.json`. Files are truncated to 100 chars per string value via `truncateRecursive`.
