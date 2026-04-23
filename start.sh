#!/usr/bin/env bash
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

ENV_FILE=".env"
PID_FILE="mmexec.pid"
PORT="${PORT:-9099}"

load_env() {
    if [[ -f "$ENV_FILE" ]]; then
        set -a
        source "$ENV_FILE"
        set +a
    fi
}

stop() {
    if [[ -f "$PID_FILE" ]]; then
        PID=$(cat "$PID_FILE")
        if kill -0 "$PID" 2>/dev/null; then
            echo "Stopping mmexec (PID $PID)..."
            kill "$PID"
            rm -f "$PID_FILE"
        else
            echo "Process $PID not running."
            rm -f "$PID_FILE"
        fi
    else
        echo "No PID file found. Trying to find and kill mmexec on port $PORT..."
        local pid
        pid=$(lsof -ti :"$PORT" 2>/dev/null || true)
        if [[ -n "$pid" ]]; then
            echo "Killing process on port $PORT (PID $pid)..."
            kill "$pid"
        else
            echo "No process found on port $PORT."
        fi
    fi
}

status() {
    if [[ -f "$PID_FILE" ]]; then
        PID=$(cat "$PID_FILE")
        if kill -0 "$PID" 2>/dev/null; then
            echo "mmexec is running (PID $PID)"
            return 0
        fi
    fi
    # Fallback: check port
    local pid
    pid=$(lsof -ti :"$PORT" 2>/dev/null || true)
    if [[ -n "$pid" ]]; then
        echo "mmexec is running (PID $pid, port $PORT)"
        return 0
    fi
    echo "mmexec is not running"
    return 1
}

start() {
    if status &>/dev/null; then
        echo "mmexec is already running."
        return 0
    fi

    if [[ -z "$MINIMAX_API_KEY" ]]; then
        echo "Error: MINIMAX_API_KEY is not set."
        echo "Set it in .env or export it before running."
        exit 1
    fi

    echo "Starting mmexec..."
    ./mmexec &
    echo $! > "$PID_FILE"
    sleep 1

    if status &>/dev/null; then
        echo "mmexec started on port $PORT (PID $(cat "$PID_FILE"))"
    else
        echo "Failed to start mmexec. Check logs."
        rm -f "$PID_FILE"
        exit 1
    fi
}

case "${1:-}" in
    --stop)
        stop
        ;;
    --status)
        status
        ;;
    *)
        load_env
        start
        ;;
esac