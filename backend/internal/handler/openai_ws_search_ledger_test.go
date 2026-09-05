package handler

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAIWSSearchLedger(t *testing.T) {
	t.Run("dedupes within attempt and accumulates across attempts", func(t *testing.T) {
		var ledger openAIWSSearchLedger
		first := ledger.observe(1, 1, 2, false, nil)
		duplicate := ledger.observe(1, 1, 2, false, nil)
		secondAttempt := ledger.observe(1, 2, 3, false, nil)

		assert.Equal(t, 2, first.CumulativeSearchCount)
		assert.Equal(t, 2, duplicate.CumulativeSearchCount)
		assert.Equal(t, 5, secondAttempt.CumulativeSearchCount)
		assert.Equal(t, 5, ledger.consume(1))
		assert.Zero(t, ledger.consume(1))
	})

	t.Run("settles failure once with final callback", func(t *testing.T) {
		var ledger openAIWSSearchLedger
		calls := 0
		settled := 0
		ledger.observe(1, 1, 1, true, func(int) {
			t.Fatal("earlier attempt callback must be replaced")
		})
		ledger.observe(1, 2, 2, true, func(total int) {
			calls++
			settled = total
		})

		require.True(t, ledger.settleFailure(1))
		assert.Equal(t, 1, calls)
		assert.Equal(t, 3, settled)
		assert.False(t, ledger.settleFailure(1))
	})

	t.Run("flush settles pending turn and leaves consumed turn alone", func(t *testing.T) {
		var ledger openAIWSSearchLedger
		settled := make(map[int]int)
		ledger.observe(1, 1, 4, false, func(total int) { settled[1] = total })
		ledger.observe(2, 1, 5, false, func(total int) { settled[2] = total })
		ledger.consume(2)
		ledger.flush()

		assert.Equal(t, map[int]int{1: 4}, settled)
	})

	t.Run("negative count clamps and overflow saturates", func(t *testing.T) {
		var ledger openAIWSSearchLedger
		negative := ledger.observe(1, 1, -2, false, nil)
		assert.Zero(t, negative.CumulativeSearchCount)

		maxInt := int(^uint(0) >> 1)
		ledger.observe(1, 1, maxInt, false, nil)
		saturated := ledger.observe(1, 2, 1, false, nil)
		assert.True(t, saturated.Saturated)
		assert.Equal(t, maxInt, saturated.CumulativeSearchCount)
	})
}

func TestOpenAIWSTurnRequestIDIsStableAndTurnScoped(t *testing.T) {
	first := openAIWSTurnRequestID("req-123", 1)
	second := openAIWSTurnRequestID("req-123", 2)
	assert.Equal(t, "req-123:turn:1", first)
	assert.Equal(t, "req-123:turn:2", second)
	assert.NotEqual(t, first, second)
}
