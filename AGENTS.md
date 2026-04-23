# AGENTS.md — Guidance for Future Claude Code Sessions

This file documents knowledge gathered during development of mmexec that is not obvious from reading the code alone. Read this before making changes.

---

## Codebase Scope

`main.go` is the **only** production file. All logic lives here. There is no sub-package structure, no `cmd/` directory, no separate package for types. Tests live in `main_test.go` in the same directory.

---

## Thinking Block Handling (Critical)

MiniMax returns thinking blocks that **look** like Anthropic thinking blocks but have self-issued signatures. These signatures are `SHA256(content)`, not Anthropic HMAC signatures. When Claude Code persists these blocks and later sends them to Anthropic, Anthropic rejects them with signature validation errors.

### The Fix

`convertThinkingToUserMessage` handles this by:

1. **Detection**: `SHA256(TrimSpace(thinking_content)) == signature` — MiniMax uses content hash as signature, so this comparison reliably identifies MiniMax blocks
2. **Fallback**: hash file at `~/.claude/mmexec/thinking/<hex_hash>` for future-proofing
3. **Conversion**: replace thinking block with `{"type": "user", "content": "previous assistant thought process: <thinking>"}`
4. **Consumption**: delete the hash file after use (one-time)

**Never strip thinking blocks outright** — conversion preserves the context that the assistant was thinking, which is information Anthropic needs.

### Whitespace Normalization

Always use `strings.TrimSpace()` before hashing thinking content. MiniMax can add trailing newlines to thinking content in JSON, and these get normalized when Claude Code retransmits the block. Without `TrimSpace`, the SHA256 won't match.

### MiniMax Response Format

MiniMax returns **SSE streaming**, not JSON. `processMiniMaxResponse` silently skips non-JSON responses. Do not assume `application/json` content-type from MiniMax.

### Signature Field Location

MiniMax thinking block signature is in the `signature` field of each block (not a top-level field). The block shape is:

```json
{"type": "thinking", "thinking": "...", "signature": "sha256_hex", "index": 0}
```

---

## Global State

`useMinimax`, `debugLevel`, and `thinkStoreBase` are package-level globals. When writing tests that modify these, **always** save and restore them:

```go
oldBase := thinkStoreBase
defer func() { thinkStoreBase = oldBase }()
// ... test code ...
```

Failure to do this causes test state to leak across test functions.

---

## Writing Tests

1. Use real JSON fixtures from `tests/` directory — they contain actual payloads from Claude Code sessions
2. Compare exact thinking content with SHA256 to verify detection logic: `SHA256(TrimSpace("The user is saying...")) == "674d62b4..."`
3. Test both detection paths: signature match AND hash file fallback
4. Test disabled think store (`thinkStoreBase = ""`) — function must not crash
5. Run tests with `go test -v ./...` and confirm **all** pass before reporting completion

---

## Routing Logic

- `mmexec` as prefix in last message → sticky routing ON, `model` forced to `MiniMax-M2.7`
- `mmrelease` as prefix in last message → sticky routing OFF
- Sticky routing ON → all subsequent requests go to MiniMax until `mmrelease`
- Sticky routing OFF + no trigger → Anthropic

The trigger is stripped from the content before forwarding.

### Literal Trigger (Teapot 418)

When the last message content is **exactly** `"mmexec"` or `"mmrelease"` (no other text), the proxy responds with HTTP 418 and does NOT forward the request upstream. The routing state is still toggled, so the **next** request goes to the new target.

This is handled by `detectTeapotTrigger` — it checks for exact equality (`==`) instead of `HasPrefix`, and takes priority over the normal `inspect` path. The 418 response uses `Content-Type: text/plain` so Claude Code renders it nicely.

The teapot message is randomly picked from one of two slices (`toMinimaxMessages` / `toAnthropicMessages`), 10 each.

---

## `forward()` Behavior

- MiniMax path: reads **full** response body, processes it, writes to client unchanged
- Anthropic path: **streams** response directly (no buffering) via `io.Copy`

This distinction matters because MiniMax responses need processing before forwarding; Anthropic responses do not.

---

## Debugging Production Issues

```sh
DEBUG=1 ./mmexec       # console logging
DEBUG=2 ./mmexec       # console + ./logs/<timestamp>-<label>.json
```

Look for `[think-store]` logs (hash file writes) and `[think-convert]` logs (conversion events) in production to confirm the thinking block pipeline is working.

---

## Key File Locations

| File | Purpose |
|---|---|
| `main.go` | All production code |
| `main_test.go` | Unit tests (15 tests) |
| `tests/multi-turn/` | Real Claude Code session JSON fixtures |
| `logs/` | `DEBUG=2` request dumps (gitignored) |
| `~/.claude/mmexec/thinking/` | Hash marker files (created at runtime) |

---

## Common Failure Modes

1. **Thinking blocks still reaching Anthropic**: likely SHA256 mismatch from whitespace difference — check `TrimSpace` is applied in both `processMiniMaxResponse` and `convertThinkingToUserMessage`
2. **`[think-store] non-JSON` logged**: MiniMax returned SSE, not JSON — this is expected and silently skipped; hash file fallback will handle it
3. **Test state leakage**: `thinkStoreBase` not restored in `defer` — check every test that sets it
4. **Forward hanging**: MiniMax path reads full body before writing; if body is large, this can be slow — profile before optimizing
