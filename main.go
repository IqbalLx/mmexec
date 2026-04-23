package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
)

const (
	trigger = "mmexec"
	anthropicBase = "https://api.anthropic.com"
	minimaxBase = "https://api.minimax.io/anthropic"
	minimaxModel = "MiniMax-M2.7"
)

// Minimal structs — only what we need to inspect/rewrite.
// Unknown fields are preserved via RawMessage.

type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type Message struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string or []ContentBlock
}

type RequestBody struct {
	Model    string            `json:"model"`
	Messages []json.RawMessage `json:"messages"`
	// Preserve every other field untouched
	Extra map[string]json.RawMessage `json:"-"`
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "9099"
	}

	minimaxKey := os.Getenv("MINIMAX_API_KEY")
	if minimaxKey == "" {
		log.Fatal("MINIMAX_API_KEY must be set")
	}

	log.Printf("llm-proxy listening on :%s", port)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read body", http.StatusBadRequest)
			return
		}
		r.Body.Close()

		// Parse into a generic map so we preserve unknown fields
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(body, &raw); err != nil {
			// Not JSON — pass through to Anthropic as-is
			forward(w, r, body, anthropicBase, "", r.Header.Get("anthropic-version"))
			return
		}

		useMinimax, raw := inspect(raw)

		rewritten, err := json.Marshal(raw)
		if err != nil {
			http.Error(w, "failed to marshal body", http.StatusInternalServerError)
			return
		}

		if useMinimax {
			log.Println("→ MiniMax (mmexec triggered)")
			forward(w, r, rewritten, minimaxBase, minimaxKey, "")
		} else {
			log.Println("→ Anthropic")
			forward(w, r, rewritten, anthropicBase, "", r.Header.Get("anthropic-version"))
		}
	})

	log.Fatal(http.ListenAndServe(":"+port, nil))
}

// inspect checks only the last message for the mmexec trigger.
// If found: strips the keyword, rewrites the model, returns useMinimax=true.
// Only the final message determines routing — prior context is preserved as-is.
func inspect(raw map[string]json.RawMessage) (bool, map[string]json.RawMessage) {
	msgsRaw, ok := raw["messages"]
	if !ok {
		return false, raw
	}

	var msgs []json.RawMessage
	if err := json.Unmarshal(msgsRaw, &msgs); err != nil {
		return false, raw
	}

	if len(msgs) == 0 {
		return false, raw
	}

	// Only inspect the last message
	lastIdx := len(msgs) - 1
	lastRaw := msgs[lastIdx]

	var last map[string]json.RawMessage
	if err := json.Unmarshal(lastRaw, &last); err != nil {
		return false, raw
	}

	contentRaw, hasContent := last["content"]
	if !hasContent {
		return false, raw
	}

	triggered := false
	var cleanedContent string

	// Content can be a string or an array of blocks
	var contentStr string
	if err := json.Unmarshal(contentRaw, &contentStr); err == nil {
		// String content
		if strings.HasPrefix(contentStr, trigger) {
			triggered = true
			cleanedContent = strings.TrimSpace(strings.TrimPrefix(contentStr, trigger))
			last["content"], _ = json.Marshal(cleanedContent)
		}
	} else {
		// Array of content blocks — only scan text blocks
		var blocks []json.RawMessage
		if err := json.Unmarshal(contentRaw, &blocks); err == nil {
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

				var text string
				if err := json.Unmarshal(block["text"], &text); err != nil {
					continue
				}

				if strings.HasPrefix(text, trigger) {
					triggered = true
					cleaned := strings.TrimSpace(strings.TrimPrefix(text, trigger))
					block["text"], _ = json.Marshal(cleaned)
					blocks[j], _ = json.Marshal(block)
				}
			}
			last["content"], _ = json.Marshal(blocks)
		}
	}

	if !triggered {
		return false, raw
	}

	// Rewrite the last message in a copy of the messages slice
	rewrittenMsgs := make([]json.RawMessage, len(msgs))
	copy(rewrittenMsgs, msgs)
	rewrittenMsgs[lastIdx], _ = json.Marshal(last)

	// Deep copy raw map and patch messages + model
	out := make(map[string]json.RawMessage, len(raw))
	for k, v := range raw {
		out[k] = v
	}
	out["messages"], _ = json.Marshal(rewrittenMsgs)
	out["model"], _ = json.Marshal(minimaxModel)

	return true, out
}

// stripSignedFields removes thinking/redacted_thinking blocks from a MiniMax response
// so they don't get sent back to Anthropic with a mismatched signature.
func stripSignedFields(data []byte) []byte {
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return data
	}
	stripRecursive(v)
	out, _ := json.Marshal(v)
	return out
}

func stripRecursive(v interface{}) {
	switch v := v.(type) {
	case map[string]interface{}:
		for k := range v {
			if k == "thinking" || k == "redacted_thinking" {
				delete(v, k)
			} else {
				stripRecursive(v[k])
			}
		}
	case []interface{}:
		for _, item := range v {
			stripRecursive(item)
		}
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

	// Strip thinking fields from MiniMax responses so they don't cause
	// "invalid signature" errors when sent back to Anthropic in the next request.
	// only strip on non-streaming responses.
	var respBody []byte
	if strings.HasSuffix(r.URL.Path, "/messages") && r.Method == http.MethodPost {
		respBody, _ = io.ReadAll(resp.Body)
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
		respBody = stripSignedFields(respBody)
	}

	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	w.Write(respBody)
}
