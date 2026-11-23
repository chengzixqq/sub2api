package service

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
)

// ErrLocalAnthropicRejected signals a completed local rejection, without retries or billing.
var ErrLocalAnthropicRejected = errors.New("local anthropic official rejection")

// NewAnthropicRequestID generates an official-format ID with 20 random base58 characters.
func NewAnthropicRequestID() string {
	const prefix = "req_011C"
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	const limit = 256 - 256%len(alphabet)
	var id [len(prefix) + 20]byte
	copy(id[:], prefix)
	for i := len(prefix); i < len(id); {
		var random [32]byte
		// crypto/rand.Read fills the buffer or terminates on entropy failure (Go 1.24+).
		_, _ = rand.Read(random[:])
		for _, value := range random {
			if int(value) >= limit {
				continue // Rejection sampling avoids modulo bias.
			}
			id[i] = alphabet[int(value)%len(alphabet)]
			i++
			if i == len(id) {
				break
			}
		}
	}
	return string(id[:])
}

// WriteLocalAnthropicInvalidRequest writes a request-scoped official error and returns
// ErrLocalAnthropicRejected. An already-started SSE response receives an error event.
func WriteLocalAnthropicInvalidRequest(c *gin.Context, message string) error {
	writeLocalAnthropicError(c, http.StatusBadRequest, "invalid_request_error", message, NewAnthropicRequestID())
	return ErrLocalAnthropicRejected
}

// WriteLocalAnthropicModelNotFound uses the same envelope for an unavailable
// model whether it was rejected by the allowlist or missing from the group.
func WriteLocalAnthropicModelNotFound(c *gin.Context, model string) {
	writeLocalAnthropicError(c, http.StatusNotFound, "not_found_error", "model: "+model, generateResponseRequestID(c))
}

func writeLocalAnthropicError(c *gin.Context, status int, errType, message, requestID string) {
	c.Header("request-id", requestID)
	MarkOpsRequestScopedError(c, errType)
	WriteAnthropicError(c, status, errType, errType, "", message, requestID, false)
}

func encodeAnthropicError(errType, code, message, id string) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	// This strings-only envelope cannot fail to encode into a bytes.Buffer.
	_ = enc.Encode(AnthropicErrorWithCodeAndRequestID(errType, code, message, id))
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// WriteAnthropicError keeps the original Ops classification across both
// transports. A queue ping may already have committed the status and headers.
func WriteAnthropicError(c *gin.Context, status int, original, wireType, code, message, id string, stream bool) {
	if id == "" {
		id = ClaudeLocalErrorID(c)
	}
	c.Set(OpsAnthropicErrorTypeKey, original)
	pending, _ := c.Get(opsNextResponseKey)
	response, preserved := pending.(opsOriginalResponse)
	if !preserved {
		prefix := ""
		if stream || gatewaySSEStarted(c) {
			prefix = "event: error\ndata: "
		}
		response = opsOriginalResponse{OpsClaudeErrorPayload(original, code, OpsOriginalErrorMessage(c, message)), prefix}
	}
	c.Set(opsOriginalResponseKey, response)
	if preserved {
		c.Set(opsNextResponseKey, nil)
	}
	MarkResponseCommitted(c)
	if stream || gatewaySSEStarted(c) {
		if response.framePrefix != "" {
			MarkOpsStreamErrorValue(c, OpsStreamError{ErrType: original, Code: code, Message: OpsOriginalErrorMessage(c, message), IntendedStatus: status, RequestScoped: IsOpsRequestScopedError(c)})
		}
		if !c.Writer.Written() {
			c.Header("Content-Type", "text/event-stream")
		}
		if _, err := c.Writer.WriteString(anthropicErrorFrame(wireType, code, message, id)); err != nil {
			_ = c.Error(err)
		}
		c.Writer.Flush()
		return
	}
	c.Header("Content-Type", "application/json")
	c.Data(status, "application/json", encodeAnthropicError(wireType, code, message, id))
}

func anthropicErrorFrame(errType, code, message, id string) string {
	return "event: error\ndata: " + string(encodeAnthropicError(errType, code, message, id)) + "\n\n"
}
