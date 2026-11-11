package admin

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func requireJSONType[T any](t *testing.T, value any) T {
	t.Helper()
	typed, ok := value.(T)
	require.True(t, ok, "expected JSON value of type %T, got %T", typed, value)
	return typed
}
