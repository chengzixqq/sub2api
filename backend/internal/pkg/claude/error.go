package claude

import "net/http"

// ErrorType returns the Messages API error type for the downstream status.
// For an already committed SSE response, pass the status of the failed operation.
func ErrorType(status int) string {
	switch status {
	case http.StatusBadRequest, http.StatusRequestTimeout, 499:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusPaymentRequired:
		return "billing_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusConflict:
		return "conflict_error"
	case http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusGatewayTimeout:
		return "timeout_error"
	case 529:
		return "overloaded_error"
	default:
		return "api_error"
	}
}
