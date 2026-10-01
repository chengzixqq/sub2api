package repository

import (
	"compress/flate"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
)

type flushableCompressor interface {
	io.WriteCloser
	Flush() error
}

func TestHTTPUpstreamCompressedReadCloseConcurrent(t *testing.T) {
	for _, encoding := range []string{"gzip", "br", "deflate", "zstd"} {
		for _, withTLS := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/tls=%v", encoding, withTLS), func(t *testing.T) {
				payload := strings.Repeat("payload", 256)
				serverErr := make(chan error, 1)
				srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Encoding", encoding)
					var writer flushableCompressor
					var err error
					switch encoding {
					case "gzip":
						writer = gzip.NewWriter(w)
					case "br":
						writer = brotli.NewWriter(w)
					case "deflate":
						writer, err = flate.NewWriter(w, flate.DefaultCompression)
					case "zstd":
						writer, err = zstd.NewWriter(w)
					}
					if err != nil {
						serverErr <- err
						return
					}
					defer func() { _ = writer.Close() }()
					if _, err = io.WriteString(writer, payload); err == nil {
						err = writer.Flush()
					}
					if err == nil {
						err = http.NewResponseController(w).Flush()
					}
					serverErr <- err
					<-r.Context().Done()
				}))
				if withTLS {
					srv.StartTLS()
				} else {
					srv.Start()
				}
				defer srv.Close()
				defer srv.CloseClientConnections()
				svc, do := lifecycleClient(t, srv, withTLS)
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
				require.NoError(t, err)
				req.Header.Set("Accept-Encoding", encoding)
				resp, err := do(req)
				require.NoError(t, err)
				require.NoError(t, <-serverErr)
				require.Empty(t, resp.Header.Get("Content-Encoding"))
				decoded := make([]byte, len(payload))
				_, err = io.ReadFull(resp.Body, decoded)
				require.NoError(t, err)
				require.Equal(t, payload, string(decoded))
				started := make(chan struct{})
				reader := &notifyReadCloser{ReadCloser: resp.Body, started: started}
				readDone := make(chan error, 1)
				go func() { _, err := reader.Read(make([]byte, 1)); readDone <- err }()
				<-started
				closeDone := make(chan error, 1)
				go func() { closeDone <- resp.Body.Close() }()
				select {
				case err := <-closeDone:
					require.NoError(t, err)
				case <-time.After(time.Second):
					t.Fatal("compressed Close did not cancel blocked Read")
				}
				select {
				case <-readDone:
				case <-time.After(time.Second):
					t.Fatal("compressed Read did not finish")
				}
				require.NoError(t, resp.Body.Close())
				require.NoError(t, ctx.Err(), "closing the response canceled the caller")
				requireNoUpstreamInFlight(t, svc)
			})
		}
	}
}
