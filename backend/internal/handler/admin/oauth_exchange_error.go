package admin

import (
	"net/http"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

// respondOAuthExchangeError answers a failed Gemini / Antigravity code exchange.
// These endpoints always returned 400 with "<prefix><error text>"; errors that
// carry a stable reason now also expose reason and metadata. Their message uses
// the ApplicationError message (not Error(), which is a debug string), so the
// Web UI keeps showing the same prefix and wording.
func respondOAuthExchangeError(c *gin.Context, messagePrefix string, err error) {
	if reason := infraerrors.Reason(err); reason != "" {
		appErr := infraerrors.FromError(err)
		response.ErrorWithDetails(c, http.StatusBadRequest, messagePrefix+appErr.Message, reason, appErr.Metadata)
		return
	}
	response.BadRequest(c, messagePrefix+err.Error())
}
