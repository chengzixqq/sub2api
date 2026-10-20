package admin

import (
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

// bindOptionalJSON binds an optional JSON request body into dst.
//
// A missing body (nil / http.NoBody) or one that is empty or whitespace only
// counts as "no body" and leaves dst untouched, so endpoints that historically
// ignored their body keep the same behavior for existing callers. Any other
// binding failure — malformed JSON or a field of the wrong type — is returned
// so the handler can reject the request before touching any state.
func bindOptionalJSON(c *gin.Context, dst any) error {
	if c.Request == nil || c.Request.Body == nil || c.Request.Body == http.NoBody {
		return nil
	}
	if err := c.ShouldBindJSON(dst); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
