package service

import "testing"

func TestRedactUpstreamURLPreservesPath(t *testing.T) {
	got := redactUpstreamURL("https://baidu.cn/v1/messages?api_key=secret")
	if got != "https://***.***/v1/messages" {
		t.Fatalf("unexpected redacted URL: %q", got)
	}
}

func TestRedactUpstreamURLs(t *testing.T) {
	got := redactUpstreamURLs(`upstream https://example.com/v1 and https://foo.bar/x`)
	want := `upstream https://***.***/v1 and https://***.***/x`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
