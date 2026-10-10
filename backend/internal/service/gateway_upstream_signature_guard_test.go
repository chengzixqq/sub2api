package service

import "testing"

func TestShouldFailoverOn400DoesNotRetrySignatureValidationFailures(t *testing.T) {
	svc := &GatewayService{}
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "invalid thinking signature",
			body: `{"type":"error","error":{"type":"invalid_request_error","message":"thinking signature is invalid"}}`,
		},
		{
			name: "forged thought signature",
			body: `{"error":{"message":"thought_signature mismatch"}}`,
		},
		{
			name: "empty signature",
			body: `{"error":{"message":"signature must not be empty"}}`,
		},
		{
			name: "beta header compatibility still fails over",
			body: `{"error":{"message":"anthropic-beta feature requires beta header"}}`,
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := svc.shouldFailoverOn400([]byte(tt.body)); got != tt.want {
				t.Fatalf("shouldFailoverOn400() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHasThinkingPayloadForRetry(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "top level thinking configuration",
			body: `{"thinking":{"type":"enabled","budget_tokens":1024}}`,
			want: true,
		},
		{
			name: "thinking message block",
			body: `{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"prior"}]}]}`,
			want: true,
		},
		{
			name: "redacted thinking message block",
			body: `{"messages":[{"role":"assistant","content":[{"type":"redacted_thinking","data":"prior"}]}]}`,
			want: true,
		},
		{
			name: "ordinary signature field",
			body: `{"messages":[{"role":"assistant","content":[{"type":"text","text":"signature is invalid"}]}]}`,
			want: false,
		},
		{
			name: "messages with string content",
			body: `{"messages":[{"role":"user","content":"signature is invalid"}]}`,
			want: false,
		},
		{
			name: "invalid json",
			body: `{not-json`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasThinkingPayloadForRetry([]byte(tt.body)); got != tt.want {
				t.Fatalf("hasThinkingPayloadForRetry() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHasSignatureSensitivePayloadForRetry(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "tool use block",
			body: `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"lookup","input":{}}]}]}`,
			want: true,
		},
		{
			name: "tool result block",
			body: `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"done"}]}]}`,
			want: true,
		},
		{
			name: "ordinary text block",
			body: `{"messages":[{"role":"assistant","content":[{"type":"text","text":"tool_use signature"}]}]}`,
		},
		{
			name: "invalid json",
			body: `{not-json`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasSignatureSensitivePayloadForRetry([]byte(tt.body)); got != tt.want {
				t.Fatalf("hasSignatureSensitivePayloadForRetry() = %v, want %v", got, tt.want)
			}
		})
	}
}
