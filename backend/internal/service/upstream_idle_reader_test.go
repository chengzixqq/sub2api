//go:build unit

package service

import (
	"io"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUpstreamIdleReader_RearmsRemainingDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newUpstreamIdleReader(strings.NewReader("abc"), time.Second)
		defer r.Stop()
		start := time.Now()
		time.Sleep(750 * time.Millisecond)
		buf := make([]byte, 1)
		n, err := r.Read(buf)
		require.NoError(t, err)
		require.Equal(t, 1, n)

		<-r.C()
		require.False(t, r.Expired())
		require.Equal(t, time.Second, time.Since(start))
		<-r.C()
		require.True(t, r.Expired())
		require.Equal(t, 1750*time.Millisecond, time.Since(start))
	})
}

func TestUpstreamIdleReader_EmptyReadsDoNotRefreshDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newUpstreamIdleReader(strings.NewReader(""), time.Second)
		defer r.Stop()
		time.Sleep(750 * time.Millisecond)
		n, err := r.Read(make([]byte, 1))
		require.Equal(t, 0, n)
		require.ErrorIs(t, err, io.EOF)
		<-r.C()
		require.True(t, r.Expired())
	})
}

func TestUpstreamIdleReader_Disabled(t *testing.T) {
	r := newUpstreamIdleReader(strings.NewReader("abc"), 0)
	defer r.Stop()
	require.Nil(t, r.C())
	require.False(t, r.Expired())
	data, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, "abc", string(data))
}

func TestUpstreamIdleReader_StopPreventsLateTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newUpstreamIdleReader(strings.NewReader(""), time.Second)
		r.Stop()
		time.Sleep(2 * time.Second)
		select {
		case <-r.C():
			t.Fatal("stopped idle timer fired")
		default:
		}
	})
}
