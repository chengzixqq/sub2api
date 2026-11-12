package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestHandleSelectionExhausted_ReadErrorAfter503StopsWithoutReset(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "database read", err: errors.New("reload selected account: database unavailable")},
		{name: "policy read", err: fmt.Errorf("load account dispatch policy: %w", errors.New("database unavailable"))},
		{name: "read deadline", err: fmt.Errorf("reload selected account: %w", context.DeadlineExceeded)},
		{name: "untyped capacity text", err: errors.New("database reported no available accounts")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := NewFailoverState(3, false)
			fs.LastFailoverErr = &service.UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable}
			fs.SwitchCount = 1
			fs.FailedAccountIDs[42] = struct{}{}
			fs.SameAccountRetryCount[42] = 2
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()

			action := fs.HandleSelectionExhausted(ctx, tc.err)

			require.Equal(t, FailoverExhausted, action)
			require.NoError(t, ctx.Err(), "read failures must not enter the 503 backoff")
			require.Equal(t, map[int64]struct{}{42: {}}, fs.FailedAccountIDs)
			require.Equal(t, 2, fs.SameAccountRetryCount[42])
			require.Equal(t, 1, fs.SwitchCount)
		})
	}
}

func TestAccountSelectionCapacityError_RecognizesWrappedSentinels(t *testing.T) {
	for _, err := range []error{
		service.ErrNoAvailableAccounts,
		service.ErrNoAvailableCompactAccounts,
		fmt.Errorf("no eligible candidates: %w", service.ErrNoAvailableAccounts),
		fmt.Errorf("compact unavailable: %w", service.ErrNoAvailableCompactAccounts),
	} {
		require.True(t, isAccountSelectionCapacityError(err))
	}
	for _, err := range []error{nil, context.Canceled, errors.New("no available accounts")} {
		require.False(t, isAccountSelectionCapacityError(err))
	}
}
