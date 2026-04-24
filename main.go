package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	trigger       = "mmexec"
	release       = "mmrelease"
	statusTrigger = "mmstatus"
	anthropicBase = "https://api.anthropic.com"
	minimaxBase   = "https://api.minimax.io/anthropic"
	minimaxModel  = "MiniMax-M2.7"
)

var (
	debugLevel     int
	thinkStoreBase string
	stateBase      string
	minimaxKey     string

	toMinimaxMessages = []string{
		"🫖 I'm a teapot! Oh hey, you enabled MiniMax — all subsequent requests now go through them.",
		"🫖 Short and stout. Here's your API key, I'll brew your next request.",
		"🫖 I'm a teapot! Oh hey, MiniMax is now the main squeeze.",
		"🫖 Can I get a refill? MiniMax is brewing your next cup of response.",
		"🫖 I'm a teapot! Oh hey, routing switched to MiniMax — enjoy the blend.",
		"🫖 Tea time is over. MiniMax is now serving your requests.",
		"🫖 I'm a teapot! Oh hey, the oracle has spoken: MiniMax it is.",
		"🫖 This teapot is all yours. MiniMax, incoming!",
		"🫖 I'm a teapot! Oh hey, MiniMax is the new pour target.",
		"🫖 Cheers! Brewing your next request with MiniMax.",
	}

	toAnthropicMessages = []string{
		"🫖 I'm a teapot! Oh hey, you disabled MiniMax — Anthropic will handle your next request.",
		"🫖 Short and stout. Pour me another API key, Anthropic here I come.",
		"🫖 I'm a teapot! Oh hey, we're back to Anthropic — the OG is back.",
		"🫖 Can I get a refill? Anthropic is handling your next request.",
		"🫖 I'm a teapot! Oh hey, routing switched to Anthropic — enjoy the blend.",
		"🫖 Tea time is over. Anthropic is now serving your requests.",
		"🫖 I'm a teapot! Oh hey, the oracle has spoken: Anthropic it is.",
		"🫖 This teapot is all yours. Anthropic, incoming!",
		"🫖 I'm a teapot! Oh hey, Anthropic is the new pour target.",
		"🫖 Cheers! Brewing your next request with Anthropic.",
	}
)

func main() {
	flag.Parse()
	switch flag.Arg(0) {
	case "proxy", "":
		runProxy()
	default:
		fmt.Printf("Usage: mmexec [proxy]\n")
	}
}

func runProxy() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "9099"
	}

	minimaxKey = os.Getenv("MINIMAX_API_KEY")
	if minimaxKey == "" {
		log.Fatal("MINIMAX_API_KEY must be set")
	}

	debugLevel = 0
	if env := os.Getenv("DEBUG"); env == "1" {
		debugLevel = 1
		log.Println("debug logging enabled (console)")
	} else if env == "2" {
		debugLevel = 2
		log.Println("debug logging enabled (console+file)")
		os.MkdirAll("logs", 0755)
	}

	initBasePaths()

	http.HandleFunc("/", handleProxy())
	log.Printf("mmexec proxy listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func initBasePaths() {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("fatal: cannot resolve user home dir: %v", err)
	}
	base := filepath.Join(home, ".claude", "mmexec")
	if err := os.MkdirAll(base, 0755); err != nil {
		log.Fatalf("fatal: failed to create base dir %s: %v", base, err)
	}
	thinkStoreBase = filepath.Join(base, "thinking")
	if err := os.MkdirAll(thinkStoreBase, 0755); err != nil {
		log.Printf("warning: failed to create think store at %s: %v; think store disabled", thinkStoreBase, err)
		thinkStoreBase = ""
	} else {
		log.Printf("thinking hash store: %s", thinkStoreBase)
	}
	stateBase = filepath.Join(base, "state")
	if err := os.MkdirAll(stateBase, 0755); err != nil {
		log.Printf("warning: failed to create state dir at %s: %v; state disabled", stateBase, err)
		stateBase = ""
	} else {
		log.Printf("state store: %s", stateBase)
	}
}

// loadState returns the useMinimax boolean for the given UUID.
// Returns (false, nil) if the state file does not exist.
// Returns (false, err) if the file exists but is unreadable.
func loadState(id string) (bool, error) {
	if stateBase == "" {
		return false, nil
	}
	path := stateFilePath(id)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	var s struct {
		UseMinimax bool `json:"useMinimax"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return false, err
	}
	return s.UseMinimax, nil
}

// saveState persists the useMinimax boolean for the given UUID.
func saveState(id string, v bool) error {
	if stateBase == "" {
		return nil
	}
	path := stateFilePath(id)
	// Ensure parent dir exists.
	if err := os.MkdirAll(stateBase, 0755); err != nil {
		return err
	}
	data, err := json.Marshal(map[string]bool{"useMinimax": v})
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func stateFilePath(id string) string {
	return filepath.Join(stateBase, id+".json")
}

func handleProxy() http.HandlerFunc {
	friendlyErrorMsg := "Oh hi! This is the mmexec proxy. It should only be accessed by Claude Code. No setup needed!"
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID := r.Header.Get("X-Claude-Code-Session-Id")
		if sessionID == "" {
			http.Error(w, friendlyErrorMsg, http.StatusBadRequest)
			return
		}

		useMinimaxNow, _ := loadState(sessionID)

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read body", http.StatusBadRequest)
			return
		}
		r.Body.Close()

		var raw map[string]json.RawMessage
		if err := json.Unmarshal(body, &raw); err != nil {
			// Non-JSON: passthrough to Anthropic directly.
			forward(w, r, body, anthropicBase, "", r.Header.Get("anthropic-version"))
			return
		}

		target, rewritten, released := inspect(raw, useMinimaxNow)

		// Persist state changes.
		if target == "minimax" && !useMinimaxNow {
			saveState(sessionID, true)
		} else if released && useMinimaxNow {
			saveState(sessionID, false)
		}

		// Detect literal "mmexec", "mmrelease", or "mmstatus" (exact word, not prefix).
		if dir := detectTeapotTrigger(raw); dir != "" {
			if dir == "to-minimax" {
				saveState(sessionID, true)
			} else if dir == "to-anthropic" {
				saveState(sessionID, false)
			}
			var msg string
			if dir == "to-minimax" {
				msg = toMinimaxMessages[rand.Intn(len(toMinimaxMessages))]
			} else if dir == "to-anthropic" {
				msg = toAnthropicMessages[rand.Intn(len(toAnthropicMessages))]
			} else {
				// mmstatus — reload state to show current provider
				useMinimaxNow, _ = loadState(sessionID)
				if useMinimaxNow {
					msg = "🫖 mmexec proxy is active — current provider: MiniMax"
				} else {
					msg = "🫖 mmexec proxy is active — current provider: Anthropic"
				}
			}
			log.Printf("[teapot] literal %s detected — status 418", dir)
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(418)
			fmt.Fprintf(w, "%s", msg)
			return
		}

		rewrittenBytes, err := json.Marshal(rewritten)
		if err != nil {
			http.Error(w, "failed to marshal body", http.StatusInternalServerError)
			return
		}

		logRequest(rewrittenBytes, target)
		dumpRequest(rewrittenBytes, target)

		if target == "minimax" {
			log.Printf("→ [minimax] session=%s path=%s", sessionID, r.URL.Path)
			forward(w, r, rewrittenBytes, minimaxBase, minimaxKey, "")
		} else {
			log.Printf("→ [anthropic] session=%s path=%s", sessionID, r.URL.Path)
			reqBody := convertThinkingToUserMessage(rewrittenBytes)
			logRequest(reqBody, "anthropic-stripped")
			dumpRequest(reqBody, "anthropic-stripped")
			forward(w, r, reqBody, anthropicBase, "", r.Header.Get("anthropic-version"))
		}
	}
}

// inspect handles routing based on per-UUID useMinimax state.
// useMinimax == true: always route to MiniMax (sticky)
// useMinimax == false: check last message for mmexec/mmrelease triggers
// Returns: target ("minimax"|"anthropic"), rewritten body, released (mmrelease detected)
func inspect(raw map[string]json.RawMessage, useMinimax bool) (string, map[string]json.RawMessage, bool) {
	msgsRaw, ok := raw["messages"]
	if !ok {
		return "anthropic", raw, false
	}

	var msgs []json.RawMessage
	if err := json.Unmarshal(msgsRaw, &msgs); err != nil {
		return "anthropic", raw, false
	}

	if len(msgs) == 0 {
		return "anthropic", raw, false
	}

	lastIdx := len(msgs) - 1
	lastRaw := msgs[lastIdx]

	var last map[string]json.RawMessage
	if err := json.Unmarshal(lastRaw, &last); err != nil {
		return "anthropic", raw, false
	}

	contentRaw, hasContent := last["content"]
	if !hasContent {
		if useMinimax {
			return "minimax", raw, false
		}
		return "anthropic", raw, false
	}

	triggered := detectTrigger(contentRaw)

	if triggered == release {
		cleanTrigger(last, contentRaw, release)
		rewrittenMsgs := make([]json.RawMessage, len(msgs))
		copy(rewrittenMsgs, msgs)
		rewrittenMsgs[lastIdx], _ = json.Marshal(last)
		out := deepCopyRaw(raw)
		out["messages"], _ = json.Marshal(rewrittenMsgs)
		return "anthropic", out, true
	}

	if triggered == trigger {
		cleanTrigger(last, contentRaw, trigger)
		rewrittenMsgs := make([]json.RawMessage, len(msgs))
		copy(rewrittenMsgs, msgs)
		rewrittenMsgs[lastIdx], _ = json.Marshal(last)
		out := deepCopyRaw(raw)
		out["messages"], _ = json.Marshal(rewrittenMsgs)
		out["model"], _ = json.Marshal(minimaxModel)
		return "minimax", out, false
	}

	if useMinimax {
		return "minimax", raw, false
	}
	return "anthropic", raw, false
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

// detectTeapotTrigger returns "to-minimax" or "to-anthropic" when the last
// message content is exactly the literal trigger word ("mmexec" or "mmrelease").
// Returns "" if no literal trigger is detected.
func detectTeapotTrigger(raw map[string]json.RawMessage) string {
	msgsRaw, ok := raw["messages"]
	if !ok {
		return ""
	}
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(msgsRaw, &messages); err != nil || len(messages) == 0 {
		return ""
	}
	last := messages[len(messages)-1]
	content, ok := last["content"]
	if !ok {
		return ""
	}

	// Try plain string first.
	var contentStr string
	if err := json.Unmarshal(content, &contentStr); err == nil {
		if contentStr == trigger {
			return "to-minimax"
		}
		if contentStr == release {
			return "to-anthropic"
		}
		if contentStr == statusTrigger {
			return "status"
		}
		return ""
	}

	// Try array-of-blocks: get last block.
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(content, &blocks); err != nil || len(blocks) == 0 {
		return ""
	}
	lastBlock := blocks[len(blocks)-1]
	if blockType, ok := lastBlock["type"]; ok {
		var t string
		json.Unmarshal(blockType, &t)
		if t != "text" {
			return ""
		}
	}
	if blockText, ok := lastBlock["text"]; ok {
		var t string
		json.Unmarshal(blockText, &t)
		if t == trigger {
			return "to-minimax"
		}
		if t == release {
			return "to-anthropic"
		}
		if t == statusTrigger {
			return "status"
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

// hashThinking returns the SHA256 hex digest of content.
func hashThinking(content string) string {
	h := sha256.New()
	h.Write([]byte(content))
	return hex.EncodeToString(h.Sum(nil))
}

// processMiniMaxResponse extracts all thinking blocks from a MiniMax API response,
// computes their SHA256 content hashes, and writes marker files to the think store.
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
			h := hashThinking(thinkingStr)
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

			userBlock := map[string]interface{}{
				"type": "text",
				"text": "previous assistant thought process: " + trimmed,
			}
			toRemove = append(toRemove, i)
			toAppend = append(toAppend, userBlock)

			log.Printf("[think-convert] converted thinking block (hash=%s, role=%s)", h[:16], role)
		}

		// Remove thinking blocks in reverse order to preserve stable indices during removal.
		for i := len(toRemove) - 1; i >= 0; i-- {
			content = append(content[:toRemove[i]], content[toRemove[i]+1:]...)
		}
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
