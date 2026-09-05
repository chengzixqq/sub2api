package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExactBalanceChangePreservesDecimal20Scale(t *testing.T) {
	before, after, delta, err := exactBalanceChange(BalanceChange{
		OldExact: "100000000.00000001",
		NewExact: "100000000.00000002",
	})

	require.NoError(t, err)
	require.Equal(t, "100000000.00000001", before.StringFixed(8))
	require.Equal(t, "100000000.00000002", after.StringFixed(8))
	require.Equal(t, "0.00000001", delta.StringFixed(8))
}

func TestExactBalanceChangeRejectsInvalidDatabaseValue(t *testing.T) {
	_, _, _, err := exactBalanceChange(BalanceChange{OldExact: "invalid", NewExact: "1.00000000"})
	require.ErrorContains(t, err, "parse exact balance before value")
}
