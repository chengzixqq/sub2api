package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

var opsEventColumnsWithSilence = []string{"id", "rule_id", "severity", "status", "title", "description", "metric_value", "threshold_value", "dimensions", "fired_at", "resolved_at", "email_sent", "created_at", "silence_id", "silence_until"}

func TestListAlertEvents_AttachesActiveSilence(t *testing.T) {
	repo, mock := newOpsSilenceRepo(t)
	fired := time.Date(2026, 10, 18, 1, 29, 0, 0, time.UTC)
	until := fired.Add(24 * time.Hour)

	mock.ExpectQuery(regexp.QuoteMeta("LEFT JOIN LATERAL")).
		WillReturnRows(sqlmock.NewRows(opsEventColumnsWithSilence).
			AddRow(int64(1), int64(41), "P1", "firing", "标准组错误率", "", 5.2, 5.0, []byte(`{"group_id":2}`), fired, nil, false, fired, int64(7), until).
			AddRow(int64(2), int64(35), "P2", "firing", "Gemini", "", nil, nil, nil, fired, nil, false, fired, nil, nil))

	events, err := repo.ListAlertEvents(context.Background(), &service.OpsAlertEventFilter{Limit: 10})
	require.NoError(t, err)
	require.Len(t, events, 2)
	require.NotNil(t, events[0].Silence)
	require.Equal(t, int64(7), events[0].Silence.ID)
	require.True(t, until.Equal(events[0].Silence.Until))
	require.Equal(t, float64(2), events[0].Dimensions["group_id"])
	require.Nil(t, events[1].Silence)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetAlertEventByID_AttachesActiveSilence(t *testing.T) {
	repo, mock := newOpsSilenceRepo(t)
	fired := time.Date(2026, 10, 18, 1, 29, 0, 0, time.UTC)
	until := fired.Add(time.Hour)

	mock.ExpectQuery(regexp.QuoteMeta("LEFT JOIN LATERAL")).WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows(opsEventColumnsWithSilence).
			AddRow(int64(1), int64(41), "P1", "firing", "t", "", nil, nil, nil, fired, nil, false, fired, int64(9), until))

	ev, err := repo.GetAlertEventByID(context.Background(), 1)
	require.NoError(t, err)
	require.NotNil(t, ev.Silence)
	require.Equal(t, int64(9), ev.Silence.ID)
}
