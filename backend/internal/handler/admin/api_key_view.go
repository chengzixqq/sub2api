package admin

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

// parseAPIKeyMaskedView reads key_view: "full" (default, the Web) keeps the
// plaintext key, "masked" leaves only key_masked. It writes a 400 and returns
// ok=false for other values.
func parseAPIKeyMaskedView(c *gin.Context) (masked bool, ok bool) {
	switch strings.TrimSpace(c.Query("key_view")) {
	case "", "full":
		return false, true
	case "masked":
		return true, true
	default:
		response.BadRequest(c, "Invalid key_view, use full/masked")
		return false, false
	}
}
