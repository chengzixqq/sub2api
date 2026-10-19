//go:build unit

package handler

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBearerToken(t *testing.T) {
	require.Equal(t, "abc.def", bearerToken("Bearer abc.def"))
	require.Equal(t, "abc", bearerToken("bearer  abc "))
	require.Equal(t, "", bearerToken("Basic abc"))
	require.Equal(t, "", bearerToken("Bearer"))
	require.Equal(t, "", bearerToken(""))
}
