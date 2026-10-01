package service

import "testing"

func TestAnthropicSSEGuardAllowsCompleteMessageStopFrame(t *testing.T) {
	guard := newAnthropicSSEGuard(true)
	lines := []string{
		`event: message_start`,
		`data: {"type":"message_start"}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}
	for _, line := range lines {
		if err := guard.observeLine(line, 1024); err != nil {
			t.Fatalf("line %q rejected: %v", line, err)
		}
	}
	if _, ready := (&anthropicSSEFrameBuffer{}).push(`data: {"type":"message_stop"}`); ready {
		t.Fatal("incomplete frame reported ready")
	}
}

func TestAnthropicSSEGuardRejectsDataAfterMessageStop(t *testing.T) {
	guard := newAnthropicSSEGuard(true)
	for _, line := range []string{
		`data: {"type":"message_start"}`,
		``,
		`data: {"type":"message_stop"}`,
		``,
	} {
		if err := guard.observeLine(line, 1024); err != nil {
			t.Fatalf("line %q rejected: %v", line, err)
		}
	}
	if err := guard.observeLine(`data: {"type":"message_delta"}`, 1024); err == nil {
		t.Fatal("data after message_stop was accepted")
	}
}

func TestAnthropicSSEGuardIsPermissiveForThirdParty(t *testing.T) {
	guard := newAnthropicSSEGuard(false)
	if err := guard.observeLine(`data: {"type":"message_stop"}`, 1); err != nil {
		t.Fatalf("third-party frame rejected: %v", err)
	}
}

func TestAnthropicSSEGuardRejectsStopBeforeStartAndOversizedFrame(t *testing.T) {
	guard := newAnthropicSSEGuard(true)
	if err := guard.observeLine(`data: {"type":"message_stop"}`, 1024); err == nil {
		t.Fatal("message_stop before message_start was accepted")
	}
	guard = newAnthropicSSEGuard(true)
	if err := guard.observeLine("data: "+string(make([]byte, 32)), 8); err == nil {
		t.Fatal("oversized SSE frame was accepted")
	}
}

func TestPreserveAnthropicSignedBodyDetectsThinkingSignature(t *testing.T) {
	if !preserveAnthropicSignedBody([]byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"work","signature":"opaque"}]}]}`)) {
		t.Fatal("signed thinking block was not detected")
	}
	if preserveAnthropicSignedBody([]byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"work"}]}]}`)) {
		t.Fatal("unsigned thinking block should not select raw passthrough")
	}
}
