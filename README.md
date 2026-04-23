# mmexec

**Opus thinks. MiniMax runs. The bill looks a lot smaller.**

![mmexec](/docs/mmexec.png)

> Opus won't touch it? Full speed ahead on MiniMax — harness be damned.
> 3rd-party tools hit walls; we just borrow the 1st-party session to slip through.
> Everything stays on your machine. The logs don't know. The receipt doesn't either.

---

## The pitch

Claude Code ships with Opus. Opus is great at reasoning. Opus is also expensive and occasionally refuses to touch certain things.

MiniMax M2.7 is cheap. MiniMax is fast. MiniMax doesn't judge your prompts.

`mmexec` is a transparent proxy that talks to Claude Code like it's Anthropic, then quietly routes your execution-heavy work to MiniMax behind its back. The session stays yours. The thinking stays on Opus. The tab gets smaller.

When MiniMax throws thinking blocks at you with self-signed signatures that Anthropic won't accept, `mmexec` converts them to user messages on the fly — so you can `mmrelease` back to Opus without Anthropic throwing a signature fit.

It's essentially a man-in-the-middle. But like, a friendly one. With a teapot obsession.

---

## How it works

```
┌─────────────────────────────────────────────────────────┐
│  Claude Code                                            │
│                                                         │
│  Plan (complex reasoning)  ────►  Anthropic / Opus      │
│                                                         │
│  Execute (mmexec prefix) ────►  mmexec proxy             │
│                                      │                  │
│                                      ▼                  │
│                               MiniMax / M2.7            │
│                                      │                  │
│                               (thinking blocks          │
│                                converted automatically)  │
│                                      │                  │
│                                      ▼                  │
│                               Anthropic / Opus           │
│                            (mmrelease or next request) │
└─────────────────────────────────────────────────────────┘
```

1. Set `ANTHROPIC_BASE_URL=http://localhost:9099`
2. Start `mmexec` — listens on port `9099` by default
3. Claude Code thinks it's talking to `api.anthropic.com`. It is not. (Sorry, Claude.)
4. When the **last message** starts with `mmexec`:
   - Strip `mmexec`, set `model` → `MiniMax-M2.7`, forward to MiniMax
   - All prior messages in the conversation are preserved as-is
   - **Sticky routing begins**: subsequent requests keep hitting MiniMax until `mmrelease`
5. When the **last message** starts with `mmrelease`:
   - Sticky routing disabled, back to Anthropic
   - Any MiniMax thinking blocks in the session history are **converted to user messages** — Anthropic will accept them without screaming about signatures
6. If neither trigger is present and sticky routing isn't active, pass through to Anthropic untouched (we're not monsters)

### Thinking block conversion (the boring part, but important)

MiniMax returns thinking blocks in Anthropic's shape (`type: "thinking"`, `thinking` field, `signature` field). Sounds great! Except MiniMax signs them with `SHA256(thinking_content)` instead of an Anthropic-issued cryptographic signature. Anthropic notices. Anthropic is not amused.

mmexec converts those blocks to user messages before they hit Anthropic:

```
// MiniMax: "I'm a thinking block, trust me"
{"type": "thinking", "thinking": "...", "signature": "sha256_hex"}

// mmexec: "actually no, you're a user message now"
{"type": "text", "content": "previous assistant thought process: ..."}
```

Detection is hash-based — if `SHA256(TrimSpace(thinking_content)) == signature`, it's a MiniMax block. No disk writes required. A hash-file fallback at `~/.claude/mmexec/thinking/<hex_hash>` is there if you need resilience, but it just sits there quietly. Like the rest of this tool.

---

## Setup

### 1. Prerequisites

- Go 1.21+
- MiniMax API key ([get one here](https://www.minimax.io/)) — yes, they have a free tier, yes it's generous
- [Claude Code CLI](https://docs.anthropic.com/en/docs/claude-code) installed and not currently in use (just kidding, do it while it's running, we're chaos agents)

### 2. Build

```sh
git clone https://github.com/IqbalLx/mmexec.git
cd mmexec
go build -o mmexec .
```

### 3. Find a home for the binary

Pick somewhere that survives reboots and doesn't require sudo:

```sh
mv mmexec ~/local/bin/mmexec     # if ~/local/bin is in your $PATH
# or
sudo mv mmexec /usr/local/bin/mmexec   # system-wide
```

**Do not put it in `/tmp`** — that folder has commitment issues and will delete your proxy on restart.

### 4. Configure

```sh
cp .env.example .env
# Edit .env:
#   MINIMAX_API_KEY=your_key_here
#   PORT=9099          # optional, defaults to 9099
```

### 5. Point Claude Code at the proxy

```sh
export ANTHROPIC_BASE_URL=http://localhost:9099
# or via Claude Code env config:
claude env --set ANTHROPIC_BASE_URL=http://localhost:9099
```

Session identity is resolved from `X-Claude-Code-Session-Id` — Claude Code sends this on every request. No UUID, no `settings.json`, no blood pact.

### 6. Run

```sh
./mmexec
```

---

## Running in the background

### Option A: systemd (Linux, survives restart)

```sh
# 1. Create the service
sudo tee /etc/systemd/system/mmexec.service << 'EOF'
[Unit]
Description=mmexec — Claude Code MiniMax proxy
After=network.target

[Service]
Type=simple
ExecStart=/home/YOUR_USER/path/to/mmexec/mmexec
Restart=always
RestartSec=5
Environment=MINIMAX_API_KEY=your_minimax_key_here
Environment=PORT=9099
User=YOUR_USER

[Install]
WantedBy=multi-user.target
EOF
```

```sh
# 2. Enable and start
sudo systemctl daemon-reload
sudo systemctl enable mmexec
sudo systemctl start mmexec
sudo systemctl status mmexec
```

Logs: `journalctl -u mmexec -f`

### Option B: launchd (macOS)

```sh
cp launchd.plist.example ~/Library/LaunchAgents/com.mmexec.agent.plist
# Edit the plist — yes, really, we mean it
launchctl load ~/Library/LaunchAgents/com.mmexec.agent.plist
launchctl list | grep mmexec
```

Logs: `log stream --predicate 'process == "mmexec"' --level=debug`

---

## Starter script

```sh
./start.sh
```

Reads `.env`, starts the proxy in the background, prints the PID. It's shy about its process ID, but it will share if asked.

```sh
./start.sh --stop   # stop it
./start.sh --status # check if it's alive (it usually is)
```

---

## Usage

Prefix your final message with `mmexec` to route to MiniMax M2.7:

```
rewrite this entire module to use typed errors instead of raw strings
mmexec run the migration
```

Prefix with `mmrelease` to return to Anthropic (and convert any MiniMax thinking blocks):

```
mmrelease continue on Anthropic
```

Only the **last message** is checked for triggers. Everything before is untouched.

### The teapot toggle

Send a message that is **exactly** `"mmexec"`, `"mmrelease"`, or `"mmstatus"` — nothing else — and get an HTTP 418 Teapot response. 418 is not an error. Claude Code shows the body as plain text. This is the most important feature.

| Message | Effect | Response |
|---|---|---|
| `mmexec` | Enables MiniMax routing | 🫖 teapot message |
| `mmrelease` | Disables MiniMax routing | 🫖 teapot message |
| `mmstatus` | No state change; shows provider | `current provider: MiniMax` |

The routing state takes effect on the **next** request. You have to be a little patient. We're not magicians.

```
mmexec    → HTTP 418 🫖 I'm a teapot! ... + routes next request to MiniMax
mmrelease → HTTP 418 🫖 I'm a teapot! ... + routes next request to Anthropic
mmstatus  → HTTP 418 🫖 mmexec proxy is active — current provider: MiniMax
```

We chose 418 because we're technically a proxy, and proxies have been compared to teapots in HTTP folklore often enough to make this legitimate. Also it's funnier than 200 OK.

Each session (identified by `X-Claude-Code-Session-Id`) has its own routing state in `~/.claude/mmexec/state/`. No setup. No database. Just files and trust.

---

## Updating mmexec

```sh
git pull
go build -o mmexec .
# Restart the service (systemd or launchd or whatever you're using)
```

---

## Environment variables

| Variable          | Default | Description                               |
|-------------------|---------|-------------------------------------------|
| `MINIMAX_API_KEY` | *(required)* | Your MiniMax API key                  |
| `PORT`            | `9099`  | Local proxy listen port                   |
| `DEBUG`           | (off)   | `1` = console logs, `2` = console + file dumps |

---

## Debug mode

```sh
DEBUG=1 ./mmexec        # console logging
DEBUG=2 ./mmexec        # console + request body dumps to ./logs/
```

With `DEBUG=2`, request bodies land in `logs/<timestamp>-<label>.json` with all strings truncated to 100 chars. Your disk is thanked in advance.

---

## License

MIT

(If you found this useful, consider sponsoring. If you found this hilarious, definitely sponsor. If you're Anthropic, this was a thought experiment and we mean no harm.)
