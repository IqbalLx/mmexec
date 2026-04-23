# mmexec

**Plan on Opus. Execute on MiniMax. Seamlessly return.**

`mmexec` is a transparent proxy that sits between Claude Code and the Anthropic API. It routes execution tasks to [MiniMax](https://www.minimax.io/) — a compatible Anthropic API endpoint — when you prefix your prompt with `mmexec`. Planning stays on Claude Opus; execution moves to MiniMax M2.7. When MiniMax produces thinking blocks, they are automatically converted to user messages so the session can safely continue on Anthropic without signature validation errors.

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

---

## How it works

1. Set `ANTHROPIC_BASE_URL=http://localhost:9099` in your environment (or via `claude env --set`)
2. Start `mmexec` — it listens on port `9099` by default
3. Claude Code talks to `localhost:9099` as if it were `api.anthropic.com`
4. When the **last message** content **starts with** `mmexec`:
   - The proxy strips the `mmexec` prefix from the prompt
   - Sets `model` → `MiniMax-M2.7`
   - Forwards the request to MiniMax's Anthropic-compatible endpoint
   - All prior messages in the conversation are preserved as-is
   - **Sticky routing begins**: subsequent requests continue to MiniMax until `mmrelease`
5. When the **last message** starts with `mmrelease`:
   - Sticky routing is disabled; future requests go to Anthropic
   - The `mmrelease` prefix is stripped before forwarding
   - Any MiniMax thinking blocks in the session history are **converted to user messages** so Anthropic accepts them without signature validation errors
6. If neither trigger is present and sticky routing is not active, the request goes to Anthropic untouched

### Thinking block conversion

MiniMax returns thinking blocks with Anthropic API shape (`type: "thinking"`, `thinking` field, `signature` field), but MiniMax's signatures are self-issued (they are `SHA256` of the thinking content, not Anthropic-issued cryptographic signatures). When Claude Code persists these blocks and later sends them to Anthropic, Anthropic rejects them.

mmexec solves this by converting MiniMax thinking blocks to user messages before forwarding to Anthropic:

```
// MiniMax returns this:
{"type": "thinking", "thinking": "...", "signature": "sha256_hex"}

// mmexec converts it to:
{"type": "text", "content": "previous assistant thought process: ..."}
```

Detection is hash-based: `SHA256(TrimSpace(thinking_content)) == signature` identifies MiniMax thinking blocks without needing to store anything on disk. A hash-file fallback at `~/.claude/mmexec/thinking/<hex_hash>` provides additional resilience.

---

## Setup

### 1. Prerequisites

- Go 1.21+
- MiniMax API key ([get one here](https://www.minimax.io/))
- [Claude Code CLI](https://docs.anthropic.com/en/docs/claude-code) installed

### 2. Build

```sh
git clone https://github.com/IqbalLx/mmexec.git
cd mmexec
go build -o mmexec .
```

### 3. Choose a permanent location

Place the binary somewhere that survives reboots and is accessible without sudo:

```sh
mv mmexec ~/local/bin/mmexec     # if ~/local/bin is in your $PATH
# or
sudo mv mmexec /usr/local/bin/mmexec   # system-wide
```

**Do not put it in `/tmp` or a download folder** — those get cleared on restart.

### 4. Configure

```sh
cp .env.example .env
# Edit .env and set:
#   MINIMAX_API_KEY=your_key_here
#   PORT=9099          # optional, defaults to 9099
```

### 5. Point Claude Code to the proxy

```sh
# Option A: environment variable
export ANTHROPIC_BASE_URL=http://localhost:9099

# Option B: via Claude Code env config
claude env --set ANTHROPIC_BASE_URL=http://localhost:9099
```

### 6. Run

```sh
./mmexec
```

---

## Running in the background

### Option A: systemd service (survives restart)

```sh
# 1. Create the service file
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
# 2. Reload systemd and enable on boot
sudo systemctl daemon-reload
sudo systemctl enable mmexec

# 3. Start now
sudo systemctl start mmexec

# 4. Check status
sudo systemctl status mmexec
```

To view logs: `journalctl -u mmexec -f`

### Option B: launchd (macOS)

```sh
cp launchd.plist.example ~/Library/LaunchAgents/com.mmexec.agent.plist
# Edit the plist and set the correct path + API key
launchctl load ~/Library/LaunchAgents/com.mmexec.agent.plist
launchctl list | grep mmexec
```

To view logs: `log stream --predicate 'process == "mmexec"' --level=debug`

---

## Starter script

```sh
./start.sh
```

Reads `.env` automatically, starts the proxy in the background, and prints the PID.

```sh
./start.sh --stop   # stop the running instance
./start.sh --status # check if it's alive
```

---

## Usage

Prefix your final message with `mmexec` to route execution to MiniMax M2.7:

```
rewrite this entire module to use typed errors instead of raw strings
mmexec run the migration
```

Prefix with `mmrelease` to return to Anthropic (and convert any MiniMax thinking blocks):

```
mmrelease continue on Anthropic
```

Only the **last message** in the conversation is checked for triggers. Conversation history is preserved — prior messages are never modified except for automatic thinking block conversion.

---

## Updating mmexec

```sh
git pull
go build -o mmexec .
# Restart the service (systemd or launchd)
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

With `DEBUG=2`, request bodies are written to `logs/<timestamp>-<label>.json` with all strings truncated to 100 chars.

---

## License

MIT
