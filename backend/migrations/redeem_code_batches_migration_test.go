package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedeemCodeBatchesMigration(t *testing.T) {
	content, err := FS.ReadFile("248_redeem_code_batches.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ALTER TABLE redeem_codes ADD COLUMN IF NOT EXISTS batch_id UUID")
	require.Contains(t, sql, "ALTER TABLE redeem_codes ADD COLUMN IF NOT EXISTS created_by BIGINT")
	require.Contains(t, sql, "CREATE INDEX IF NOT EXISTS idx_redeem_codes_batch_id ON redeem_codes (batch_id) WHERE batch_id IS NOT NULL")
	require.Contains(t, sql, "CREATE INDEX IF NOT EXISTS idx_redeem_codes_created_at ON redeem_codes (created_at DESC, id DESC)")
	require.NotContains(t, sql, "+goose")
}
