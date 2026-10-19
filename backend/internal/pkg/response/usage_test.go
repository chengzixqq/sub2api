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

func TestUsageIncrementalShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	UsageIncremental(c, []int{3, 2}, 50, true, 1, 3, usagequery.Metadata{Timezone: "UTC"})
	var payload struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	require.Nil(t, payload.Data["total"])
	require.Nil(t, payload.Data["pages"])
	require.Equal(t, false, payload.Data["total_exact"])
	require.Equal(t, true, payload.Data["has_more"])
	require.EqualValues(t, 1, payload.Data["page"])
	require.EqualValues(t, 50, payload.Data["page_size"])
	require.EqualValues(t, 1, payload.Data["since_id"])
	require.EqualValues(t, 3, payload.Data["latest_id"])
	require.Len(t, payload.Data["items"], 2)
	require.Contains(t, payload.Data, "query")
}

func TestPaginatedPayloadMatchesPaginated(t *testing.T) {
	require.Equal(t, PaginatedData{Items: []int{}, Total: 0, Page: 1, PageSize: 20, Pages: 1}, PaginatedPayload([]int{}, 0, 1, 20))
	require.Equal(t, 3, PaginatedPayload(nil, 41, 1, 20).Pages)
}
