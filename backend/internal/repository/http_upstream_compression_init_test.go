package repository

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHTTPUpstreamCompressedHeadersReturnBeforeBody(t *testing.T) {
	for _, encoding := range []string{"gzip", "zstd"} {
		for _, withTLS := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/tls=%v", encoding, withTLS), func(t *testing.T) {
				srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Encoding", encoding)
					w.WriteHeader(http.StatusOK)
					_ = http.NewResponseController(w).Flush()
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
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
				require.NoError(t, err)
				req.Header.Set("Accept-Encoding", encoding)
				type result struct {
					resp *http.Response
					err  error
				}
				done := make(chan result, 1)
				go func() { resp, err := do(req); done <- result{resp, err} }()
				select {
				case got := <-done:
					require.NoError(t, got.err)
					require.NoError(t, got.resp.Body.Close())
					_, err = got.resp.Body.Read(make([]byte, 1))
					require.Error(t, err, "Close before Read must not initialize the decoder")
					require.NoError(t, ctx.Err())
				case <-time.After(200 * time.Millisecond):
					cancel()
					got := <-done
					if got.resp != nil {
						_ = got.resp.Body.Close()
					}
					t.Error("Do waited for a compression header before the stream idle timer could start")
				}
				requireNoUpstreamInFlight(t, svc)
			})
		}
	}
}

func TestDecompressResponseInvalidHeadersPreserveBody(t *testing.T) {
	for _, encoding := range []string{"gzip", "zstd"} {
		t.Run(encoding, func(t *testing.T) {
			partialHeader := []byte{0x1f, 0x8b}
			if encoding == "zstd" {
				partialHeader = []byte{0x28, 0xb5}
			}
			for _, payload := range [][]byte{[]byte("not a compressed response"), partialHeader, nil} {
				resp := newEncodedResponse(encoding, payload)
				decompressResponseBody(resp)
				require.Empty(t, resp.Header.Get("Content-Encoding"), "metadata must be normalized before the first Read")
				body, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				require.Equal(t, string(payload), string(body))
				require.NoError(t, resp.Body.Close())
			}
		})
	}
}

func TestDecompressedBodyCloseInterruptsDecoderInitialization(t *testing.T) {
	for _, encoding := range []string{"gzip", "zstd"} {
		for _, partial := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/partial=%v", encoding, partial), func(t *testing.T) {
				pr, pw := io.Pipe()
				defer func() { _ = pw.Close() }()
				readStarted := make(chan struct{})
				original := &notifyReadCloser{ReadCloser: pr, started: readStarted}
				resp := &http.Response{Header: http.Header{"Content-Encoding": []string{encoding}}, Body: original}
				decompressResponseBody(resp)
				readDone := make(chan error, 1)
				go func() { _, err := io.ReadAll(resp.Body); readDone <- err }()
				<-readStarted
				if partial {
					prefix := []byte{0x1f, 0x8b}
					if encoding == "zstd" {
						prefix = []byte{0x28, 0xb5}
					}
					// Pipe writes complete only once initialization has consumed the prefix.
					_, err := pw.Write(prefix)
					require.NoError(t, err)
				}
				closeDone := make(chan error, 1)
				go func() { closeDone <- resp.Body.Close() }()
				select {
				case err := <-closeDone:
					require.NoError(t, err)
				case <-time.After(time.Second):
					t.Fatal("Close did not interrupt decoder initialization")
				}
				select {
				case err := <-readDone:
					require.Error(t, err)
				case <-time.After(time.Second):
					t.Fatal("decoder initialization left a blocked Read")
				}
				require.NoError(t, resp.Body.Close())
			})
		}
	}
}
