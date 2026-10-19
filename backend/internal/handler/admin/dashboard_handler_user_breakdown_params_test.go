package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

const userBreakdownTestBase = "/admin/dashboard/user-breakdown?start_date=2026-03-01&end_date=2026-03-16"

func serveUserBreakdown(t *testing.T, extraQuery string) (*userBreakdownRepoCapture, *httptest.ResponseRecorder) {
	t.Helper()
	repo := &userBreakdownRepoCapture{}
	router := newUserBreakdownRouter(repo)
	req := httptest.NewRequest(http.MethodGet, userBreakdownTestBase+extraQuery, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return repo, w
}

func decodeUserBreakdownErrorMessage(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var resp struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, http.StatusBadRequest, resp.Code)
	return resp.Message
}

func TestGetUserBreakdown_InvalidFilterReturnsNamedMessage(t *testing.T) {
	cases := []struct {
		query string
		want  string
	}{
		{"&group_id=abc", "Invalid group_id"},
		{"&user_id=abc", "Invalid user_id"},
		{"&api_key_id=1.5", "Invalid api_key_id"},
		{"&account_id=x", "Invalid account_id"},
		{"&billing_type=abc", "Invalid billing_type"},
		{"&billing_type=999", "Invalid billing_type"},
		{"&billing_mode=bogus", "Invalid billing_mode"},
		{"&stream=abc", "Invalid stream, use true or false"},
		{"&native_compaction_v2=maybe", "Invalid native_compaction_v2, use true or false"},
		{"&upstream_model_mismatch=maybe", "Invalid upstream_model_mismatch, use true or false"},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			repo, w := serveUserBreakdown(t, tc.query)

			require.Equal(t, http.StatusBadRequest, w.Code)
			message := decodeUserBreakdownErrorMessage(t, w)
			require.Equal(t, tc.want, message)
			require.NotContains(t, message, "strconv")
			require.Zero(t, repo.capturedLimit, "repository must not be queried")
		})
	}
}

func TestGetUserBreakdown_PaddedNumericFiltersMatchPlain(t *testing.T) {
	plainRepo, plainW := serveUserBreakdown(t,
		"&group_id=12&user_id=7&api_key_id=8&account_id=9&billing_type=1&native_compaction_v2=true")
	paddedRepo, paddedW := serveUserBreakdown(t,
		"&group_id=%2012&user_id=%207&api_key_id=8%20&account_id=%209&billing_type=%201&native_compaction_v2=%20true")

	require.Equal(t, http.StatusOK, plainW.Code)
	require.Equal(t, http.StatusOK, paddedW.Code)
	require.Equal(t, plainRepo.capturedDim, paddedRepo.capturedDim)

	dim := paddedRepo.capturedDim
	require.Equal(t, int64(12), dim.GroupID)
	require.Equal(t, int64(7), dim.UserID)
	require.Equal(t, int64(8), dim.APIKeyID)
	require.Equal(t, int64(9), dim.AccountID)
	require.NotNil(t, dim.BillingType)
	require.Equal(t, int8(1), *dim.BillingType)
	require.NotNil(t, dim.NativeCompactionV2)
	require.True(t, *dim.NativeCompactionV2)
}

func TestGetUserBreakdown_RequestTypeWinsOverStream(t *testing.T) {
	repo, w := serveUserBreakdown(t, "&request_type=sync&stream=true")

	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, repo.capturedDim.RequestType)
	require.Equal(t, int16(service.RequestTypeSync), *repo.capturedDim.RequestType)
	require.Nil(t, repo.capturedDim.Stream, "request_type takes precedence over stream")

	streamOnlyRepo, streamOnlyW := serveUserBreakdown(t, "&stream=false")

	require.Equal(t, http.StatusOK, streamOnlyW.Code)
	require.Nil(t, streamOnlyRepo.capturedDim.RequestType)
	require.NotNil(t, streamOnlyRepo.capturedDim.Stream)
	require.False(t, *streamOnlyRepo.capturedDim.Stream)
}

func TestGetUserBreakdown_InvalidStreamRejectedWithRequestType(t *testing.T) {
	repo, w := serveUserBreakdown(t, "&request_type=stream&stream=abc")

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Equal(t, "Invalid stream, use true or false", decodeUserBreakdownErrorMessage(t, w))
	require.Zero(t, repo.capturedLimit, "repository must not be queried")

	blankRepo, blankW := serveUserBreakdown(t, "&request_type=stream&stream=%20")

	require.Equal(t, http.StatusOK, blankW.Code)
	require.NotNil(t, blankRepo.capturedDim.RequestType)
	require.Equal(t, int16(service.RequestTypeStream), *blankRepo.capturedDim.RequestType)
	require.Nil(t, blankRepo.capturedDim.Stream)
}

func TestGetUserBreakdown_InvalidLimitRejected(t *testing.T) {
	for _, limit := range []string{"0", "-1", "201", "999", "abc", "1.5"} {
		t.Run(limit, func(t *testing.T) {
			repo, w := serveUserBreakdown(t, "&model=test&limit="+limit)

			require.Equal(t, http.StatusBadRequest, w.Code)
			require.Equal(t, "Invalid limit, use 1-200", decodeUserBreakdownErrorMessage(t, w))
			require.Zero(t, repo.capturedLimit, "repository must not be queried")
		})
	}
}

func TestGetUserBreakdown_LimitBoundaries(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  int
	}{
		{"min", "&limit=1", 1},
		{"max", "&limit=200", 200},
		{"absent", "", 50},
		{"empty", "&limit=", 50},
		{"padded", "&limit=%20100", 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, w := serveUserBreakdown(t, tc.query)

			require.Equal(t, http.StatusOK, w.Code)
			require.Equal(t, tc.want, repo.capturedLimit)
		})
	}
}

func TestGetUserBreakdown_EmptyModelSourceDefaultsToRequested(t *testing.T) {
	repo, w := serveUserBreakdown(t, "&model=m&model_source=")

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "m", repo.capturedDim.Model)
	require.Equal(t, usagestats.ModelSourceRequested, repo.capturedDim.ModelType)
}

func TestGetUserBreakdown_PassesSharedFiltersThrough(t *testing.T) {
	repo, w := serveUserBreakdown(t,
		"&billing_mode=token&request_id=r1&upstream_model_mismatch=true&native_compaction_v2=false")

	require.Equal(t, http.StatusOK, w.Code)
	dim := repo.capturedDim
	require.Equal(t, "token", dim.BillingMode)
	require.Equal(t, "r1", dim.RequestID)
	require.NotNil(t, dim.UpstreamModelMismatch)
	require.True(t, *dim.UpstreamModelMismatch)
	require.NotNil(t, dim.NativeCompactionV2)
	require.False(t, *dim.NativeCompactionV2)
}
