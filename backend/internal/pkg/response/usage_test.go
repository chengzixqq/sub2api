package response

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagequery"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUsagePaginatedDeferredNeverClaimsExactTotal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	UsagePaginated(c, []int{1, 2}, 3, 1, 2, true, usagequery.Metadata{Timezone: "UTC"})
	var payload struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	require.Nil(t, payload.Data["total"])
	require.Nil(t, payload.Data["pages"])
	require.Equal(t, false, payload.Data["total_exact"])
	require.Equal(t, true, payload.Data["has_more"])
}
