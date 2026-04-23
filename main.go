package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	trigger       = "mmexec"
	release       = "mmrelease"
	anthropicBase = "https://api.anthropic.com"
	minimaxBase   = "https://api.minimax.io/anthropic"
	minimaxModel  = "MiniMax-M2.7"
)

// Global state for sticky MiniMax routing across requests.
var (
	useMinimax     bool   // true = keep routing to MiniMax until mmrelease
	debugLevel     int    // 0=off, 1=console, 2=console+file dumps
	thinkStoreBase string // set in initThinkStore, e.g. ~/.claude/mmexec/thinking/
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "9099"
	}

	minimaxKey := os.Getenv("MINIMAX_API_KEY")
	if minimaxKey == "" {
		log.Fatal("MINIMAX_API_KEY must be set")
	}

	log.Printf("mmexec listening on :%s", port)
	debugLevel = 0
	if env := os.Getenv("DEBUG"); env == "1" {
		debugLevel = 1
		log.Println("debug logging enabled (console)")
	} else if env == "2" {
		debugLevel = 2
		log.Println("debug logging enabled (console+file)")
		os.MkdirAll("logs", 0755)
	}

	initThinkStore()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read body", http.StatusBadRequest)
			return
		}
		r.Body.Close()

		var raw map[string]json.RawMessage
		if err := json.Unmarshal(body, &raw); err != nil {
			forward(w, r, body, anthropicBase, "", r.Header.Get("anthropic-version"))
			return
		}

		target, raw := inspect(raw)

		rewritten, err := json.Marshal(raw)
		if err != nil {
			http.Error(w, "failed to marshal body", http.StatusInternalServerError)
			return
		}

		logRequest(rewritten, target)
		dumpRequest(rewritten, target)

		if target == "minimax" {
			log.Println("→ MiniMax")
			forward(w, r, rewritten, minimaxBase, minimaxKey, "")
		} else {
			log.Println("→ Anthropic")
			reqBody := convertThinkingToUserMessage(rewritten)
			logRequest(reqBody, "anthropic-stripped")
			dumpRequest(reqBody, "anthropic-stripped")
			forward(w, r, reqBody, anthropicBase, "", r.Header.Get("anthropic-version"))
		}
	})

	log.Fatal(http.ListenAndServe(":"+port, nil))
}

// inspect handles routing based on global useMinimax state.
// - useMinimax == true: always route to MiniMax (sticky)
// - useMinimax == false: check last message for mmexec/mmrelease triggers
func inspect(raw map[string]json.RawMessage) (string, map[string]json.RawMessage) {
	msgsRaw, ok := raw["messages"]
	if !ok {
		return "anthropic", raw
	}

	var msgs []json.RawMessage
	if err := json.Unmarshal(msgsRaw, &msgs); err != nil {
		return "anthropic", raw
	}

	if len(msgs) == 0 {
		return "anthropic", raw
	}

	lastIdx := len(msgs) - 1
	lastRaw := msgs[lastIdx]

	var last map[string]json.RawMessage
	if err := json.Unmarshal(lastRaw, &last); err != nil {
		return "anthropic", raw
	}

	contentRaw, hasContent := last["content"]
	if !hasContent {
		if useMinimax {
			return "minimax", raw
		}
		return "anthropic", raw
	}

	triggered := detectTrigger(contentRaw)

	if triggered == release {
		useMinimax = false
		cleanTrigger(last, contentRaw, release)
		rewrittenMsgs := make([]json.RawMessage, len(msgs))
		copy(rewrittenMsgs, msgs)
		rewrittenMsgs[lastIdx], _ = json.Marshal(last)
		out := deepCopyRaw(raw)
		out["messages"], _ = json.Marshal(rewrittenMsgs)
		return "anthropic", out
	}

	if triggered == trigger {
		useMinimax = true
		cleanTrigger(last, contentRaw, trigger)
		rewrittenMsgs := make([]json.RawMessage, len(msgs))
		copy(rewrittenMsgs, msgs)
		rewrittenMsgs[lastIdx], _ = json.Marshal(last)
		out := deepCopyRaw(raw)
		out["messages"], _ = json.Marshal(rewrittenMsgs)
		out["model"], _ = json.Marshal(minimaxModel)
		return "minimax", out
	}

	if useMinimax {
		return "minimax", raw
	}
	return "anthropic", raw
}

// detectTrigger checks content for mmexec or mmrelease prefix.
// Returns "" if neither is found.
func detectTrigger(contentRaw json.RawMessage) string {
	var contentStr string
	if err := json.Unmarshal(contentRaw, &contentStr); err == nil {
		if strings.HasPrefix(contentStr, trigger) {
			return trigger
		}
		if strings.HasPrefix(contentStr, release) {
			return release
		}
		return ""
	}

	var blocks []json.RawMessage
	if err := json.Unmarshal(contentRaw, &blocks); err != nil {
		return ""
	}
	for _, blockRaw := range blocks {
		var block map[string]json.RawMessage
		if err := json.Unmarshal(blockRaw, &block); err != nil {
			continue
		}
		typeRaw, _ := block["type"]
		var blockType string
		json.Unmarshal(typeRaw, &blockType)
		if blockType != "text" {
			continue
		}
		textRaw, ok := block["text"]
		if !ok {
			continue
		}
		var text string
		if err := json.Unmarshal(textRaw, &text); err != nil {
			continue
		}
		if strings.HasPrefix(text, trigger) {
			return trigger
		}
		if strings.HasPrefix(text, release) {
			return release
		}
	}
	return ""
}

// cleanTrigger strips the given trigger prefix from the last message content.
// Works for both string content and array-of-blocks content.
func cleanTrigger(last map[string]json.RawMessage, contentRaw json.RawMessage, triggerToStrip string) {
	var contentStr string
	if err := json.Unmarshal(contentRaw, &contentStr); err == nil {
		cleaned := strings.TrimSpace(strings.TrimPrefix(contentStr, triggerToStrip))
		last["content"], _ = json.Marshal(cleaned)
		return
	}

	var blocks []json.RawMessage
	if err := json.Unmarshal(contentRaw, &blocks); err != nil {
		return
	}
	for j, blockRaw := range blocks {
		var block map[string]json.RawMessage
		if err := json.Unmarshal(blockRaw, &block); err != nil {
			continue
		}
		typeRaw, _ := block["type"]
		var blockType string
		json.Unmarshal(typeRaw, &blockType)
		if blockType != "text" {
			continue
		}
		textRaw, ok := block["text"]
		if !ok {
			continue
		}
		var text string
		if err := json.Unmarshal(textRaw, &text); err != nil {
			continue
		}
		if strings.HasPrefix(text, triggerToStrip) {
			cleaned := strings.TrimSpace(strings.TrimPrefix(text, triggerToStrip))
			block["text"], _ = json.Marshal(cleaned)
			blocks[j], _ = json.Marshal(block)
		}
	}
	last["content"], _ = json.Marshal(blocks)
}

func deepCopyRaw(raw map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(raw))
	for k, v := range raw {
		out[k] = v
	}
	return out
}

// logRequest prints the request body if debug logging is enabled.
// label describes the phase (e.g. "raw", "anthropic-stripped").
func logRequest(body []byte, label string) {
	if debugLevel < 1 {
		return
	}
	var v interface{}
	if err := json.Unmarshal(body, &v); err != nil {
		log.Printf("[debug %s] (invalid json: %v)", label, err)
		return
	}
	msgs, _ := v.(map[string]interface{})["messages"].([]interface{})
	if msgs == nil {
		log.Printf("[debug %s] no messages array", label)
		return
	}
	log.Printf("[debug %s] messages count: %d", label, len(msgs))
	for i, m := range msgs {
		mi, _ := m.(map[string]interface{})
		role, _ := mi["role"].(string)
		model, _ := mi["model"].(string)
		content := "<unknown>"
		if c, ok := mi["content"]; ok {
			if cs, ok := c.(string); ok {
				content = cs
			} else if ca, ok := c.([]interface{}); ok {
				types := make([]string, 0)
				for _, b := range ca {
					if bm, ok := b.(map[string]interface{}); ok {
						types = append(types, bm["type"].(string))
					}
				}
				content = strings.Join(types, ",")
			}
		}
		log.Printf("  [%d] role=%s model=%s content=%s", i, role, model, truncate(content, 80))
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// initThinkStore creates the thinking hash store directory at ~/.claude/mmexec/thinking/.
// If the home directory cannot be resolved or the directory cannot be created,
// thinkStoreBase is set to "" which causes all store operations to silently no-op.
func initThinkStore() {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Printf("warning: cannot resolve user home dir: %v; hash store disabled", err)
		thinkStoreBase = ""
		return
	}
	thinkStoreBase = filepath.Join(home, ".claude", "mmexec", "thinking")
	if err := os.MkdirAll(thinkStoreBase, 0755); err != nil {
		log.Printf("warning: failed to create thinking hash store at %s: %v; hash store disabled", thinkStoreBase, err)
		thinkStoreBase = ""
		return
	}
	log.Printf("thinking hash store: %s", thinkStoreBase)
}

// hashThinking returns the SHA256 hex digest of content.
// Used as the filename in the think store to uniquely identify a thinking block.
func hashThinking(content string) string {
	h := sha256.New()
	h.Write([]byte(content))
	return hex.EncodeToString(h.Sum(nil))
}

// processMiniMaxResponse extracts all thinking blocks from a MiniMax API response,
// computes their SHA256 content hashes, and writes marker files to the think store.
// Each marker file is an empty file named by the hash — existence marks a block as
// "pending"; it will be consumed exactly once when the same content appears in an
// Anthropic request. Errors are logged but do not block response forwarding.
func processMiniMaxResponse(body []byte) error {
	if thinkStoreBase == "" {
		return nil
	}

	var resp struct {
		Content []json.RawMessage `json:"content"`
	}
	if len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		// Non-JSON responses (error pages, HTML, plain text) can't contain thinking blocks; skip.
		log.Printf("[think-store] non-JSON MiniMax response (%d bytes): %v", len(body), err)
		return nil
	}

	for _, blockRaw := range resp.Content {
		var block struct {
			Type     string `json:"type"`
			Thinking string `json:"thinking"`
		}
		if err := json.Unmarshal(blockRaw, &block); err != nil {
			continue
		}
		if block.Type != "thinking" || block.Thinking == "" {
			continue
		}

		// Trim trailing/leading whitespace before hashing to ensure the hash is stable
		// regardless of how whitespace is normalized when Claude Code persists the block.
		h := hashThinking(strings.TrimSpace(block.Thinking))
		path := filepath.Join(thinkStoreBase, h)
		if err := os.WriteFile(path, nil, 0644); err != nil {
			log.Printf("[think-store] failed to write hash file %s: %v", path, err)
			continue
		}
		log.Printf("[think-store] stored hash for thinking block (len=%d, hash=%s)", len(block.Thinking), h[:16])
	}
	return nil
}

// convertThinkingToUserMessage scans all assistant messages for thinking blocks whose
// content hash exists in the think store. For each match:
//   - The thinking block is removed from the content array
//   - A replacement user block is appended: {"type": "user", "content": "previous assistant thought process: <thinking>"}
//   - The hash file is deleted (one-time use per thinking block)
//
// If the think store is unavailable, returns body unchanged.
// Only assistant messages are processed; no other fields are modified.
func convertThinkingToUserMessage(body []byte) []byte {
	var v interface{}
	if err := json.Unmarshal(body, &v); err != nil {
		return body
	}

	msgs, ok := v.(map[string]interface{})["messages"].([]interface{})
	if !ok {
		return body
	}

	for _, msgRaw := range msgs {
		msg, ok := msgRaw.(map[string]interface{})
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if role != "assistant" {
			continue
		}

		content, ok := msg["content"].([]interface{})
		if !ok {
			continue
		}

		var toRemove []int
		var toAppend []interface{}

		for i, block := range content {
			blockMap, ok := block.(map[string]interface{})
			if !ok {
				continue
			}
			if blockMap["type"] != "thinking" {
				continue
			}
			thinkingStr, ok := blockMap["thinking"].(string)
			if !ok || thinkingStr == "" {
				continue
			}

			trimmed := strings.TrimSpace(thinkingStr)
			if trimmed == "" {
				continue
			}
			h := hashThinking(trimmed)
			sig, _ := blockMap["signature"].(string)

			isMiniMaxBySig := sig != "" && sig == h
			isMiniMaxByHash := false
			if thinkStoreBase != "" {
				hashPath := filepath.Join(thinkStoreBase, h)
				if _, err := os.Stat(hashPath); err == nil {
					os.Remove(hashPath)
					isMiniMaxByHash = true
				}
			}

			if !isMiniMaxBySig && !isMiniMaxByHash {
				continue
			}

			userContent := trimmed

			userBlock := map[string]interface{}{
				"type": "text",
				"text": "previous assistant thought process: " + userContent,
			}
			toRemove = append(toRemove, i)
			toAppend = append(toAppend, userBlock)

			log.Printf("[think-convert] converted thinking block (hash=%s, role=%s)", h[:16], role)
		}

		// Remove thinking blocks in reverse order to preserve stable indices during removal.
		for i := len(toRemove) - 1; i >= 0; i-- {
			content = append(content[:toRemove[i]], content[toRemove[i]+1:]...)
		}
		// Append replacement user blocks after all removals.
		msg["content"] = append(content, toAppend...)
	}

	out, err := json.Marshal(v)
	if err != nil {
		return body
	}
	return out
}

// dumpRequest writes the full request body to logs/<timestamp>-<label>.json
// with all strings truncated to 100 chars for readability.
func dumpRequest(body []byte, label string) {
	if debugLevel < 2 {
		return
	}
	var v interface{}
	if err := json.Unmarshal(body, &v); err != nil {
		log.Printf("[dump] failed to unmarshal %s: %v", label, err)
		return
	}
	sanitized := truncateRecursive(v)
	filename := fmt.Sprintf("logs/%d-%s.json", time.Now().UnixNano(), label)
	f, err := os.Create(filename)
	if err != nil {
		log.Printf("[dump] failed to write %s: %v", filename, err)
		return
	}
	defer f.Close()
	json.NewEncoder(f).Encode(sanitized)
	log.Printf("[dump] wrote %s", filename)
}

// truncateRecursive returns a deep copy of v with all strings truncated to 100 chars.
func truncateRecursive(v interface{}) interface{} {
	switch v := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{})
		for k2, v2 := range v {
			out[k2] = truncateRecursive(v2)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(v))
		for i, v2 := range v {
			out[i] = truncateRecursive(v2)
		}
		return out
	case string:
		return truncate(v, 100)
	default:
		return v
	}
}

func forward(w http.ResponseWriter, r *http.Request, body []byte, baseURL, apiKey, anthropicVersion string) {
	url := baseURL + r.URL.Path
	if r.URL.RawQuery != "" {
		url += "?" + r.URL.RawQuery
	}

	req, err := http.NewRequest(r.Method, url, bytes.NewReader(body))
	if err != nil {
		http.Error(w, "failed to create request", http.StatusInternalServerError)
		return
	}

	skip := map[string]bool{
		"host":              true,
		"x-forwarded-for":   true,
		"x-forwarded-host":  true,
		"x-forwarded-proto": true,
		"via":               true,
		"forwarded":         true,
	}
	for k, vals := range r.Header {
		if skip[strings.ToLower(k)] {
			continue
		}
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}

	req.Header.Set("Content-Type", "application/json")

	if apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	if anthropicVersion != "" {
		req.Header.Set("anthropic-version", anthropicVersion)
	} else {
		req.Header.Del("anthropic-version")
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// For MiniMax responses: read the full body to extract and store thinking block hashes
	// before forwarding. Anthropic responses are streamed directly.
	if baseURL == minimaxBase {
		respBodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			http.Error(w, "failed to read upstream response: "+err.Error(), http.StatusBadGateway)
			return
		}
		processMiniMaxResponse(respBodyBytes)
		for k, vals := range resp.Header {
			for _, v := range vals {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		w.Write(respBodyBytes)
		return
	}

	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
