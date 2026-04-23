package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHashThinking(t *testing.T) {
	h := hashThinking("hello world")
	expected := sha256Hash("hello world")
	if h != expected {
		t.Errorf("hashThinking(%q) = %q; want %q", "hello world", h, expected)
	}
}

func sha256Hash(s string) string {
	h := sha256.New()
	h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))
}

func TestProcessMiniMaxResponse(t *testing.T) {
	oldBase := thinkStoreBase
	defer func() { thinkStoreBase = oldBase }()
	tmpDir := t.TempDir()
	thinkStoreBase = tmpDir

	respBody := `{
		"id": "minimax-response-1",
		"content": [
			{
				"type": "thinking",
				"thinking": "I need to implement a hash-based think store",
				"signature": "minimax_sig_abc123",
				"index": 0
			},
			{
				"type": "text",
				"text": "Here's my implementation plan..."
			}
		]
	}`

	err := processMiniMaxResponse([]byte(respBody))
	if err != nil {
		t.Fatalf("processMiniMaxResponse returned error: %v", err)
	}

	h := sha256Hash("I need to implement a hash-based think store")
	hashPath := filepath.Join(tmpDir, h)
	if _, err := os.Stat(hashPath); os.IsNotExist(err) {
		t.Errorf("expected hash file %q to exist; got not exist", h[:16])
	}
}

func TestConvertThinkingToUserMessage(t *testing.T) {
	oldBase := thinkStoreBase
	defer func() { thinkStoreBase = oldBase }()
	tmpDir := t.TempDir()
	thinkStoreBase = tmpDir

	thinkingContent := "I need to implement a hash-based think store"
	h := sha256Hash(thinkingContent)
	hashPath := filepath.Join(tmpDir, h)
	if err := os.WriteFile(hashPath, nil, 0644); err != nil {
		t.Fatalf("failed to write hash file: %v", err)
	}

	input := map[string]interface{}{
		"model": "claude-sonnet-4-6",
		"messages": []interface{}{
			map[string]interface{}{
				"role":    "user",
				"content": "How do I implement a think store?",
			},
			map[string]interface{}{
				"role": "assistant",
				"content": []interface{}{
					map[string]interface{}{
						"type":     "thinking",
						"thinking": thinkingContent,
					},
					map[string]interface{}{
						"type": "text",
						"text": "Here's my implementation plan...",
					},
				},
			},
		},
		"tools": []interface{}{
			map[string]interface{}{"name": "bash", "description": "Run bash commands"},
			map[string]interface{}{"name": "read", "description": "Read files"},
		},
		"metadata": map[string]interface{}{
			"user_id": "user-123",
		},
	}
	inputJSON, _ := json.Marshal(input)

	result := convertThinkingToUserMessage(inputJSON)

	var out map[string]interface{}
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatalf("convertThinkingToUserMessage returned invalid JSON: %v", err)
	}

	// Verify tools array is intact (not nulled out)
	tools, ok := out["tools"].([]interface{})
	if !ok {
		t.Fatalf("tools field missing or wrong type; got %T", out["tools"])
	}
	if len(tools) != 2 {
		t.Errorf("tools array length = %d; want 2", len(tools))
	}
	if tools[0].(map[string]interface{})["name"] != "bash" {
		t.Errorf("tools[0].name = %v; want 'bash'", tools[0].(map[string]interface{})["name"])
	}

	// Verify metadata intact
	meta, ok := out["metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("metadata missing or wrong type; got %T", out["metadata"])
	}
	if meta["user_id"] != "user-123" {
		t.Errorf("metadata.user_id = %v; want 'user-123'", meta["user_id"])
	}

	// Verify model intact
	if out["model"] != "claude-sonnet-4-6" {
		t.Errorf("model = %v; want 'claude-sonnet-4-6'", out["model"])
	}

	// Verify messages array: assistant message has thinking block converted to user message
	msgs, ok := out["messages"].([]interface{})
	if !ok {
		t.Fatalf("messages missing or wrong type; got %T", out["messages"])
	}
	if len(msgs) != 2 {
		t.Errorf("messages length = %d; want 2", len(msgs))
	}

	userMsg := msgs[0].(map[string]interface{})
	if userMsg["role"] != "user" {
		t.Errorf("messages[0].role = %v; want 'user'", userMsg["role"])
	}

	asstMsg := msgs[1].(map[string]interface{})
	if asstMsg["role"] != "assistant" {
		t.Errorf("messages[1].role = %v; want 'assistant'", asstMsg["role"])
	}
	content := asstMsg["content"].([]interface{})
	if len(content) != 2 {
		t.Errorf("messages[1].content length = %d; want 2 (thinking removed, text kept, user appended)", len(content))
	}
	if content[0].(map[string]interface{})["type"] != "text" {
		t.Errorf("content[0].type = %v; want 'text'", content[0].(map[string]interface{})["type"])
	}
	userBlock := content[1].(map[string]interface{})
	if userBlock["type"] != "text" {
		t.Errorf("content[1].type = %v; want 'text' (replacement text block)", userBlock["type"])
	}
	expectedContent := "previous assistant thought process: " + thinkingContent
	if userBlock["text"] != expectedContent {
		t.Errorf("content[1].text = %q; want %q", userBlock["text"], expectedContent)
	}

	// Verify hash file was deleted (one-time use)
	if _, err := os.Stat(hashPath); !os.IsNotExist(err) {
		t.Errorf("hash file should be deleted after conversion; still exists")
	}
}

func TestConvertThinkingToUserMessage_NoHashMatch(t *testing.T) {
	oldBase := thinkStoreBase
	defer func() { thinkStoreBase = oldBase }()
	tmpDir := t.TempDir()
	thinkStoreBase = tmpDir

	input := map[string]interface{}{
		"model": "claude-sonnet-4-6",
		"messages": []interface{}{
			map[string]interface{}{
				"role": "assistant",
				"content": []interface{}{
					map[string]interface{}{
						"type":     "thinking",
						"thinking": "Some thinking from Anthropic",
					},
					map[string]interface{}{
						"type": "text",
						"text": "Response from Anthropic",
					},
				},
			},
		},
	}
	inputJSON, _ := json.Marshal(input)

	result := convertThinkingToUserMessage(inputJSON)

	var out map[string]interface{}
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatalf("convertThinkingToUserMessage returned invalid JSON: %v", err)
	}

	msgs := out["messages"].([]interface{})
	asstMsg := msgs[0].(map[string]interface{})
	content := asstMsg["content"].([]interface{})

	// Thinking block should remain unchanged (no hash match)
	if len(content) != 2 {
		t.Errorf("content length = %d; want 2 (thinking block preserved since no hash match)", len(content))
	}
	if content[0].(map[string]interface{})["type"] != "thinking" {
		t.Errorf("content[0].type = %v; want 'thinking' (preserved)", content[0].(map[string]interface{})["type"])
	}
}

func TestConvertThinkingToUserMessage_PreservesNonAssistantMessages(t *testing.T) {
	oldBase := thinkStoreBase
	defer func() { thinkStoreBase = oldBase }()
	tmpDir := t.TempDir()
	thinkStoreBase = tmpDir

	thinkingContent := "MiniMax was here"
	h := sha256Hash(thinkingContent)
	hashPath := filepath.Join(tmpDir, h)
	os.WriteFile(hashPath, nil, 0644)

	input := map[string]interface{}{
		"model": "claude-sonnet-4-6",
		"messages": []interface{}{
			map[string]interface{}{
				"role":    "system",
				"content": "You are a helpful assistant.",
			},
			map[string]interface{}{
				"role":    "user",
				"content": "Hello",
			},
			map[string]interface{}{
				"role": "assistant",
				"content": []interface{}{
					map[string]interface{}{
						"type":     "thinking",
						"thinking": thinkingContent,
					},
					map[string]interface{}{
						"type": "text",
						"text": "Hi there!",
					},
				},
			},
			map[string]interface{}{
				"role":    "user",
				"content": "How are you?",
			},
		},
		"stream":     true,
		"max_tokens": 4096,
		"context_management": map[string]interface{}{
			"edits": []interface{}{"edit-1", "edit-2"},
		},
	}
	inputJSON, _ := json.Marshal(input)

	result := convertThinkingToUserMessage(inputJSON)

	var out map[string]interface{}
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatalf("convertThinkingToUserMessage returned invalid JSON: %v", err)
	}

	// system, user, user messages should be intact
	msgs := out["messages"].([]interface{})
	if msgs[0].(map[string]interface{})["role"] != "system" {
		t.Errorf("messages[0] role = %v; want 'system'", msgs[0].(map[string]interface{})["role"])
	}
	if msgs[1].(map[string]interface{})["role"] != "user" {
		t.Errorf("messages[1] role = %v; want 'user'", msgs[1].(map[string]interface{})["role"])
	}
	if msgs[3].(map[string]interface{})["role"] != "user" {
		t.Errorf("messages[3] role = %v; want 'user'", msgs[3].(map[string]interface{})["role"])
	}

	// stream and max_tokens intact
	if out["stream"] != true {
		t.Errorf("stream = %v; want true", out["stream"])
	}
	if float64(out["max_tokens"].(float64)) != 4096 {
		t.Errorf("max_tokens = %v; want 4096", out["max_tokens"])
	}

	// context_management intact
	cm := out["context_management"].(map[string]interface{})
	edits := cm["edits"].([]interface{})
	if len(edits) != 2 {
		t.Errorf("context_management.edits length = %d; want 2", len(edits))
	}

	// assistant message: thinking removed, user block appended
	asstMsg := msgs[2].(map[string]interface{})
	content := asstMsg["content"].([]interface{})
	if len(content) != 2 {
		t.Errorf("assistant content length = %d; want 2", len(content))
	}
	if content[0].(map[string]interface{})["type"] != "text" {
		t.Errorf("content[0].type = %v; want 'text'", content[0].(map[string]interface{})["type"])
	}
	if content[1].(map[string]interface{})["type"] != "text" {
		t.Errorf("content[1].type = %v; want 'text'", content[1].(map[string]interface{})["type"])
	}
}

func TestConvertThinkingToUserMessage_ThinkStoreDisabled(t *testing.T) {
	oldBase := thinkStoreBase
	defer func() { thinkStoreBase = oldBase }()
	thinkStoreBase = ""

	input := map[string]interface{}{
		"model": "claude-sonnet-4-6",
		"messages": []interface{}{
			map[string]interface{}{
				"role": "assistant",
				"content": []interface{}{
					map[string]interface{}{
						"type":     "thinking",
						"thinking": "Some thinking",
					},
				},
			},
		},
	}
	inputJSON, _ := json.Marshal(input)

	result := convertThinkingToUserMessage(inputJSON)

	var out map[string]interface{}
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatalf("convertThinkingToUserMessage returned invalid JSON: %v", err)
	}

	msgs := out["messages"].([]interface{})
	content := msgs[0].(map[string]interface{})["content"].([]interface{})
	// With disabled store, thinking block should be preserved
	if len(content) != 1 {
		t.Errorf("with disabled store, thinking block should be preserved; got length %d", len(content))
	}
}

// TestRealFileSignatureHeuristic uses the exact thinking content and signature
// from the multi-turn test (1776930410106561000-anthropic.json) to verify the
// SHA256 signature heuristic: when SHA256(thinking) == signature, it's from MiniMax.
func TestRealFileSignatureHeuristic(t *testing.T) {
	oldBase := thinkStoreBase
	defer func() { thinkStoreBase = oldBase }()
	// The exact data from the real multi-turn scenario.
	thinkingContent := `The user is saying "hi" - a simple greeting. I should respond in a friendly but concise way.`
	// SHA256 of thinkingContent (verified against real signature in the test file).
	signature := `674d62b4cf559147f369c7b9a149db15a30e208773a2c06fca6ab3d492068fdc`

	// Simulate the Anthropic request with the thinking block from the real file.
	anthropicReq := map[string]interface{}{
		"model": "claude-sonnet-4-6",
		"messages": []interface{}{
			map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": "mmexec hi"},
				},
			},
			map[string]interface{}{
				"role": "assistant",
				"content": []interface{}{
					map[string]interface{}{
						"type":      "thinking",
						"thinking":  thinkingContent,
						"signature": signature,
					},
					map[string]interface{}{
						"type": "text",
						"text": "Hi! How can I help you today?",
					},
				},
			},
			map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": "hi"},
				},
			},
		},
		"tools": []interface{}{
			map[string]interface{}{"name": "Bash", "description": "Run bash commands"},
		},
	}
	reqJSON, _ := json.Marshal(anthropicReq)

	// With no hash files, detection relies solely on the signature heuristic.
	thinkStoreBase = "" // explicitly disable the hash store
	result := convertThinkingToUserMessage(reqJSON)

	var out map[string]interface{}
	json.Unmarshal(result, &out)

	msgs := out["messages"].([]interface{})

	// Assistant message: thinking block removed, user block appended.
	asstMsg := msgs[1].(map[string]interface{})
	content := asstMsg["content"].([]interface{})

	if len(content) != 2 {
		t.Errorf("content length = %d; want 2 (text kept + user appended)", len(content))
	}
	if content[0].(map[string]interface{})["type"] != "text" {
		t.Errorf("content[0].type = %v; want 'text'", content[0].(map[string]interface{})["type"])
	}
	if content[1].(map[string]interface{})["type"] != "text" {
		t.Errorf("content[1].type = %v; want 'text' (signature heuristic converted it)", content[1].(map[string]interface{})["type"])
	}

	userBlock := content[1].(map[string]interface{})
	expectedContent := "previous assistant thought process: " + thinkingContent
	if userBlock["text"] != expectedContent {
		t.Errorf("user block text mismatch; got %q", userBlock["text"])
	}

	// tools array must be intact.
	tools := out["tools"].([]interface{})
	if len(tools) != 1 {
		t.Errorf("tools length = %d; want 1", len(tools))
	}
	if tools[0].(map[string]interface{})["name"] != "Bash" {
		t.Errorf("tools[0].name = %v; want 'Bash'", tools[0].(map[string]interface{})["name"])
	}
	t.Logf("signature heuristic correctly identified MiniMax thinking block")
}

// TestRealFileConcreteThinkContent uses the exact thinking content from
// tests/1776929980960225000-anthropic.json to verify the round-trip.
func TestRealFileConcreteThinkContent(t *testing.T) {
	oldBase := thinkStoreBase
	defer func() { thinkStoreBase = oldBase }()
	tmpDir := t.TempDir()
	thinkStoreBase = tmpDir

	// The exact thinking string from the real file.
	thinkingContent := `The user just said "hi" - a simple greeting. I should respond briefly and naturally.`
	thinkingSignature := `94c132ee35460e72e0cf53a87d5ef5f671a2d5a8b96a06c5a3edefd7aef967f7`

	// Simulate MiniMax response with that exact thinking block.
	minimaxResp := `{
		"content": [
			{
				"type": "thinking",
				"thinking": "The user just said \"hi\" - a simple greeting. I should respond briefly and naturally.",
				"signature": "94c132ee35460e72e0cf53a87d5ef5f671a2d5a8b96a06c5a3edefd7aef967f7",
				"index": 0
			},
			{
				"type": "text",
				"text": "Hi! How can I help you today?"
			}
		]
	}`

	// Store the hash as MiniMax response would.
	processMiniMaxResponse([]byte(minimaxResp))

	// Verify hash file was created.
	h := sha256Hash(strings.TrimSpace(thinkingContent))
	hashPath := filepath.Join(tmpDir, h)
	if _, err := os.Stat(hashPath); os.IsNotExist(err) {
		t.Fatalf("hash file not created: hash=%s", h[:16])
	}
	t.Logf("hash file created: %s", h[:16])

	// Now simulate the Anthropic request with the same thinking block
	// (as Claude Code would send it back from conversation history).
	anthropicReq := map[string]interface{}{
		"model": "claude-sonnet-4-6",
		"messages": []interface{}{
			map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": "mmexec hi"},
				},
			},
			map[string]interface{}{
				"role": "assistant",
				"content": []interface{}{
					map[string]interface{}{
						"type":      "thinking",
						"thinking":  thinkingContent,
						"signature": thinkingSignature,
					},
					map[string]interface{}{
						"type": "text",
						"text": "Hi! How can I help you today?",
					},
				},
			},
			map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{
						"type":          "text",
						"text":          "hi",
						"cache_control": map[string]interface{}{"type": "ephemeral", "ttl": "1h"},
					},
				},
			},
		},
		"tools": []interface{}{
			map[string]interface{}{"name": "Bash", "description": "Execute bash commands"},
		},
	}
	reqJSON, _ := json.Marshal(anthropicReq)

	result := convertThinkingToUserMessage(reqJSON)

	var out map[string]interface{}
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	msgs := out["messages"].([]interface{})

	// Assistant message: thinking block removed, user block appended.
	asstMsg := msgs[1].(map[string]interface{})
	content := asstMsg["content"].([]interface{})

	if len(content) != 2 {
		t.Errorf("content length = %d; want 2 (text kept + user appended)", len(content))
	}
	if content[0].(map[string]interface{})["type"] != "text" {
		t.Errorf("content[0].type = %v; want 'text'", content[0].(map[string]interface{})["type"])
	}
	if content[1].(map[string]interface{})["type"] != "text" {
		t.Errorf("content[1].type = %v; want 'text'", content[1].(map[string]interface{})["type"])
	}

	userBlock := content[1].(map[string]interface{})
	expectedContent := "previous assistant thought process: " + thinkingContent
	if userBlock["text"] != expectedContent {
		t.Errorf("user block text = %q; want %q", userBlock["text"], expectedContent)
	}

	// tools array must be intact.
	tools := out["tools"].([]interface{})
	if len(tools) != 1 {
		t.Errorf("tools length = %d; want 1", len(tools))
	}
	if tools[0].(map[string]interface{})["name"] != "Bash" {
		t.Errorf("tools[0].name = %v; want 'Bash'", tools[0].(map[string]interface{})["name"])
	}

	// Hash file should be deleted (one-time use).
	if _, err := os.Stat(hashPath); !os.IsNotExist(err) {
		t.Errorf("hash file should be deleted after conversion; still exists")
	}
	t.Logf("hash file correctly deleted after conversion")
}

func TestProcessMiniMaxResponse_StoresTrimmedContent(t *testing.T) {
	oldBase := thinkStoreBase
	defer func() { thinkStoreBase = oldBase }()
	tmpDir := t.TempDir()
	thinkStoreBase = tmpDir

	// MiniMax response has trailing newline in thinking content.
	respBody := `{
		"content": [
			{"type": "thinking", "thinking": "I should respond briefly.\n"}
		]
	}`

	processMiniMaxResponse([]byte(respBody))

	// Hash is stored from TRIMMED content (no trailing newline).
	h := sha256Hash("I should respond briefly.") // trimmed
	hashPath := filepath.Join(tmpDir, h)
	if _, err := os.Stat(hashPath); os.IsNotExist(err) {
		t.Errorf("expected hash file for trimmed content %q to exist; got not exist", h[:16])
	}

	// Hash file for untrimmed content should NOT exist.
	hUntrimmed := sha256Hash("I should respond briefly.\n")
	hashPathUntrimmed := filepath.Join(tmpDir, hUntrimmed)
	if _, err := os.Stat(hashPathUntrimmed); !os.IsNotExist(err) {
		t.Errorf("hash file for untrimmed content should NOT exist")
	}
}

func TestConvertThinkingToUserMessage_TrailingWhitespaceMismatch(t *testing.T) {
	oldBase := thinkStoreBase
	defer func() { thinkStoreBase = oldBase }()
	tmpDir := t.TempDir()
	thinkStoreBase = tmpDir

	// Think store has hash for TRIMMED content (as stored from MiniMax response).
	thinkingTrimmed := "I should respond briefly."
	h := sha256Hash(thinkingTrimmed)
	os.WriteFile(filepath.Join(tmpDir, h), nil, 0644)

	// Claude Code sends thinking content WITH trailing newline (as it was in MiniMax response JSON).
	thinkingUntrimmed := "I should respond briefly.\n"

	input := map[string]interface{}{
		"messages": []interface{}{
			map[string]interface{}{
				"role": "assistant",
				"content": []interface{}{
					map[string]interface{}{
						"type":     "thinking",
						"thinking": thinkingUntrimmed, // has trailing newline
					},
					map[string]interface{}{
						"type": "text",
						"text": "Hi!",
					},
				},
			},
		},
	}
	inputJSON, _ := json.Marshal(input)

	result := convertThinkingToUserMessage(inputJSON)

	var out map[string]interface{}
	json.Unmarshal(result, &out)

	msgs := out["messages"].([]interface{})
	content := msgs[0].(map[string]interface{})["content"].([]interface{})

	// Thinking block should be converted despite whitespace mismatch.
	if len(content) != 2 {
		t.Errorf("content length = %d; want 2 (thinking removed, user block appended)", len(content))
	}
	if content[0].(map[string]interface{})["type"] != "text" {
		t.Errorf("content[0].type = %v; want 'text'", content[0].(map[string]interface{})["type"])
	}
	if content[1].(map[string]interface{})["type"] != "text" {
		t.Errorf("content[1].type = %v; want 'text' (converted despite trailing newline)", content[1].(map[string]interface{})["type"])
	}
	if content[1].(map[string]interface{})["text"] != "previous assistant thought process: "+strings.TrimSpace(thinkingUntrimmed) {
		t.Errorf("user block text mismatch; got %q", content[1].(map[string]interface{})["text"])
	}

	// Hash file should be deleted.
	if _, err := os.Stat(filepath.Join(tmpDir, h)); !os.IsNotExist(err) {
		t.Errorf("hash file should be deleted after conversion")
	}
}

func TestProcessMiniMaxResponse_MultipleThinkingBlocks(t *testing.T) {
	oldBase := thinkStoreBase
	defer func() { thinkStoreBase = oldBase }()
	tmpDir := t.TempDir()
	thinkStoreBase = tmpDir

	respBody := `{
		"content": [
			{"type": "thinking", "thinking": "First thought"},
			{"type": "thinking", "thinking": "Second thought"},
			{"type": "text", "text": "Done thinking"}
		]
	}`

	err := processMiniMaxResponse([]byte(respBody))
	if err != nil {
		t.Fatalf("processMiniMaxResponse returned error: %v", err)
	}

	h1 := sha256Hash("First thought")
	h2 := sha256Hash("Second thought")

	if _, err := os.Stat(filepath.Join(tmpDir, h1)); os.IsNotExist(err) {
		t.Errorf("hash for 'First thought' not stored")
	}
	if _, err := os.Stat(filepath.Join(tmpDir, h2)); os.IsNotExist(err) {
		t.Errorf("hash for 'Second thought' not stored")
	}
}

func TestConvertThinkingToUserMessage_MultipleThinkingBlocks(t *testing.T) {
	oldBase := thinkStoreBase
	defer func() { thinkStoreBase = oldBase }()
	tmpDir := t.TempDir()
	thinkStoreBase = tmpDir

	h1 := sha256Hash("First thought")
	h2 := sha256Hash("Second thought")
	os.WriteFile(filepath.Join(tmpDir, h1), nil, 0644)
	os.WriteFile(filepath.Join(tmpDir, h2), nil, 0644)

	input := map[string]interface{}{
		"messages": []interface{}{
			map[string]interface{}{
				"role": "assistant",
				"content": []interface{}{
					map[string]interface{}{"type": "thinking", "thinking": "First thought"},
					map[string]interface{}{"type": "thinking", "thinking": "Second thought"},
					map[string]interface{}{"type": "text", "text": "Final text"},
				},
			},
		},
	}
	inputJSON, _ := json.Marshal(input)

	result := convertThinkingToUserMessage(inputJSON)

	var out map[string]interface{}
	json.Unmarshal(result, &out)

	msgs := out["messages"].([]interface{})
	content := msgs[0].(map[string]interface{})["content"].([]interface{})

	// Original: [thinking1, thinking2, text]
	// After: [text, user1, user2]  (thinking removed in reverse order, then user blocks appended)
	if len(content) != 3 {
		t.Errorf("content length = %d; want 3 (1 text + 2 user replacements)", len(content))
	}
	if content[0].(map[string]interface{})["type"] != "text" {
		t.Errorf("content[0].type = %v; want 'text'", content[0].(map[string]interface{})["type"])
	}
	if content[1].(map[string]interface{})["type"] != "text" {
		t.Errorf("content[1].type = %v; want 'text'", content[1].(map[string]interface{})["type"])
	}
	if content[2].(map[string]interface{})["type"] != "text" {
		t.Errorf("content[2].type = %v; want 'text'", content[2].(map[string]interface{})["type"])
	}
}
