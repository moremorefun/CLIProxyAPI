package executor

import (
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

func TestParseAntigravityRetryDelay_Valid429Response(t *testing.T) {
	body := []byte(`{
		"error": {
			"code": 429,
			"message": "Resource exhausted",
			"details": [
				{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "10.5s"}
			]
		}
	}`)

	result := parseAntigravityRetryDelay(body)
	if result == nil {
		t.Fatal("Expected non-nil duration")
	}
	expected := 10*time.Second + 500*time.Millisecond
	if *result != expected {
		t.Errorf("Expected %v, got %v", expected, *result)
	}
}

func TestParseAntigravityRetryDelay_LargeDuration(t *testing.T) {
	body := []byte(`{
		"error": {
			"details": [
				{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "10627.493230411s"}
			]
		}
	}`)

	result := parseAntigravityRetryDelay(body)
	if result == nil {
		t.Fatal("Expected non-nil duration")
	}
	// Verify it's roughly 2.95 hours
	if result.Hours() < 2.9 || result.Hours() > 3.0 {
		t.Errorf("Expected ~2.95 hours, got %v", *result)
	}
}

func TestParseAntigravityRetryDelay_NegativeCases(t *testing.T) {
	testCases := []struct {
		name string
		body []byte
	}{
		{"nil body", nil},
		{"empty body", []byte{}},
		{"no details field", []byte(`{"error": {"code": 429, "message": "Resource exhausted"}}`)},
		{"empty details array", []byte(`{"error": {"details": []}}`)},
		{"wrong @type", []byte(`{"error": {"details": [{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": "RATE_LIMIT"}]}}`)},
		{"empty retryDelay", []byte(`{"error": {"details": [{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": ""}]}}`)},
		{"invalid duration format", []byte(`{"error": {"details": [{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "invalid"}]}}`)},
		{"zero duration", []byte(`{"error": {"details": [{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "0s"}]}}`)},
		{"negative duration", []byte(`{"error": {"details": [{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "-5s"}]}}`)},
		{"invalid json", []byte(`{invalid json`)},
		{"details not array", []byte(`{"error": {"details": "not an array"}}`)},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := parseAntigravityRetryDelay(tc.body)
			if result != nil {
				t.Errorf("Expected nil, got %v", *result)
			}
		})
	}
}

func TestParseAntigravityRetryDelay_MultipleDetails(t *testing.T) {
	body := []byte(`{
		"error": {
			"details": [
				{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": "RATE_LIMIT"},
				{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "30s"},
				{"@type": "type.googleapis.com/google.rpc.Help", "links": []}
			]
		}
	}`)

	result := parseAntigravityRetryDelay(body)
	if result == nil {
		t.Fatal("Expected non-nil duration")
	}
	expected := 30 * time.Second
	if *result != expected {
		t.Errorf("Expected %v, got %v", expected, *result)
	}
}

func TestNewAntigravityStatusErr_429WithRetryDelay(t *testing.T) {
	body := []byte(`{
		"error": {
			"details": [
				{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "60s"}
			]
		}
	}`)

	err := newAntigravityStatusErr(429, body)
	if err.code != 429 {
		t.Errorf("Expected code 429, got %d", err.code)
	}
	if err.retryAfter == nil {
		t.Fatal("Expected non-nil retryAfter for 429")
	}
	expected := 60 * time.Second
	if *err.retryAfter != expected {
		t.Errorf("Expected retryAfter %v, got %v", expected, *err.retryAfter)
	}
}

func TestNewAntigravityStatusErr_Non429(t *testing.T) {
	body := []byte(`{"error": {"message": "Internal error"}}`)

	err := newAntigravityStatusErr(500, body)
	if err.code != 500 {
		t.Errorf("Expected code 500, got %d", err.code)
	}
	if err.retryAfter != nil {
		t.Errorf("Expected nil retryAfter for non-429, got %v", *err.retryAfter)
	}
}

func TestNewAntigravityStatusErr_429WithoutRetryInfo(t *testing.T) {
	body := []byte(`{"error": {"message": "Rate limit exceeded"}}`)

	err := newAntigravityStatusErr(429, body)
	if err.code != 429 {
		t.Errorf("Expected code 429, got %d", err.code)
	}
	if err.retryAfter != nil {
		t.Errorf("Expected nil retryAfter when no RetryInfo, got %v", *err.retryAfter)
	}
}

func TestInjectAntigravitySystemInstruction_NoExistingInstruction(t *testing.T) {
	// Test case: no existing systemInstruction
	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)

	result := injectAntigravitySystemInstruction(payload)

	// Verify systemInstruction was created
	if !gjson.GetBytes(result, "request.systemInstruction").Exists() {
		t.Fatal("Expected systemInstruction to be created")
	}

	// Verify it has exactly one part (Antigravity's prompt)
	parts := gjson.GetBytes(result, "request.systemInstruction.parts")
	if !parts.IsArray() || len(parts.Array()) != 1 {
		t.Fatalf("Expected exactly 1 part, got %d", len(parts.Array()))
	}

	// Verify the content contains Antigravity's identity
	text := gjson.GetBytes(result, "request.systemInstruction.parts.0.text").String()
	if !strings.Contains(text, "Antigravity") || !strings.Contains(text, "<identity>") {
		t.Error("Expected Antigravity system prompt content")
	}
}

func TestInjectAntigravitySystemInstruction_WithExistingInstruction(t *testing.T) {
	// Test case: existing systemInstruction should be preserved
	payload := []byte(`{
		"request": {
			"systemInstruction": {
				"role": "user",
				"parts": [
					{"text": "You are a helpful assistant"},
					{"text": "Always respond in JSON"}
				]
			},
			"contents": [{"role":"user","parts":[{"text":"hello"}]}]
		}
	}`)

	result := injectAntigravitySystemInstruction(payload)

	// Verify systemInstruction exists
	if !gjson.GetBytes(result, "request.systemInstruction").Exists() {
		t.Fatal("Expected systemInstruction to exist")
	}

	// Verify it has 3 parts: Antigravity + 2 user parts
	parts := gjson.GetBytes(result, "request.systemInstruction.parts")
	if !parts.IsArray() || len(parts.Array()) != 3 {
		t.Fatalf("Expected exactly 3 parts, got %d", len(parts.Array()))
	}

	// Verify first part is Antigravity's prompt
	firstText := gjson.GetBytes(result, "request.systemInstruction.parts.0.text").String()
	if !strings.Contains(firstText, "Antigravity") {
		t.Error("Expected first part to be Antigravity's prompt")
	}

	// Verify second and third parts are user's original prompts
	secondText := gjson.GetBytes(result, "request.systemInstruction.parts.1.text").String()
	if secondText != "You are a helpful assistant" {
		t.Errorf("Expected second part to be 'You are a helpful assistant', got '%s'", secondText)
	}

	thirdText := gjson.GetBytes(result, "request.systemInstruction.parts.2.text").String()
	if thirdText != "Always respond in JSON" {
		t.Errorf("Expected third part to be 'Always respond in JSON', got '%s'", thirdText)
	}
}

func TestInjectAntigravitySystemInstruction_EmptyParts(t *testing.T) {
	// Test case: systemInstruction exists but parts array is empty
	payload := []byte(`{
		"request": {
			"systemInstruction": {
				"role": "user",
				"parts": []
			},
			"contents": [{"role":"user","parts":[{"text":"hello"}]}]
		}
	}`)

	result := injectAntigravitySystemInstruction(payload)

	// Verify it has exactly 1 part (Antigravity's prompt)
	parts := gjson.GetBytes(result, "request.systemInstruction.parts")
	if !parts.IsArray() || len(parts.Array()) != 1 {
		t.Fatalf("Expected exactly 1 part, got %d", len(parts.Array()))
	}
}

