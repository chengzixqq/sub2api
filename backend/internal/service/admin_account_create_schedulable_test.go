package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// A new account can start outside the scheduler so untested credentials never receive live traffic: the state is
// written by the same INSERT, so there is no window in which the account is schedulable.
func TestBuildAccountForCreate_Schedulable(t *testing.T) {
	ctx := WithScope(context.Background(), AdminScope())
	base := CreateAccountInput{Name: "t", Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "sk-x"}}

	defaulted, err := buildAccountForCreate(ctx, &base, nil)
	require.NoError(t, err)
	require.True(t, defaulted.Schedulable, "not sending the field keeps the old behaviour")

	off := false
	in := base
	in.Schedulable = &off
	paused, err := buildAccountForCreate(ctx, &in, nil)
	require.NoError(t, err)
	require.False(t, paused.Schedulable)
	require.Equal(t, StatusActive, paused.Status, "only scheduling is off; the account is not disabled")
}
