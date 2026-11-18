package logger

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestInferStdLogLevel(t *testing.T) {
	cases := []struct {
		msg  string
		want Level
	}{
		{msg: "Warning: queue full", want: LevelWarn},
		{msg: "Forward request failed: timeout", want: LevelError},
		{msg: "[ERROR] upstream unavailable", want: LevelError},
		{msg: "[OpenAI WS Mode] reconnect_retry account_id=22 retry=1 max_retries=5", want: LevelInfo},
		{msg: "service started", want: LevelInfo},
		{msg: "debug: cache miss", want: LevelDebug},
		{msg: "[DashboardAggregation] usage_logs 保留清理失败: pq: canceling statement due to statement timeout", want: LevelError},
		{msg: "[DashboardAggregation] usage_logs 保留清理完成 deleted=120", want: LevelInfo},
		{msg: "[Cleanup] 重试仍失败 (warning threshold reached)", want: LevelError},
		{msg: "[warn] 清理失败，稍后重试", want: LevelWarn},
		{msg: "warning: 同步失败", want: LevelWarn},
		{msg: "[debug] 刷新失败", want: LevelDebug},
	}

	for _, tc := range cases {
		got := inferStdLogLevel(tc.msg)
		if got != tc.want {
			t.Fatalf("inferStdLogLevel(%q)=%v want=%v", tc.msg, got, tc.want)
		}
	}
}

func TestNormalizeStdLogMessage(t *testing.T) {
	raw := "  [TokenRefresh]  cycle complete \n total=1   failed=0 \n"
	got := normalizeStdLogMessage(raw)
	want := "[TokenRefresh] cycle complete total=1 failed=0"
	if got != want {
		t.Fatalf("normalizeStdLogMessage()=%q want=%q", got, want)
	}
}

func TestStdLogBridgeRoutesLevels(t *testing.T) {
	origStdout := os.Stdout
	origStderr := os.Stderr
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	os.Stdout = stdoutW
	os.Stderr = stderrW
	t.Cleanup(func() {
		os.Stdout = origStdout
		os.Stderr = origStderr
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		_ = stderrR.Close()
		_ = stderrW.Close()
	})

	if err := Init(InitOptions{
		Level:       "debug",
		Format:      "json",
		ServiceName: "sub2api",
		Environment: "test",
		Output: OutputOptions{
			ToStdout: true,
			ToFile:   false,
		},
		Sampling: SamplingOptions{Enabled: false},
	}); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	log.Printf("service started")
	log.Printf("Warning: queue full")
	log.Printf("Forward request failed: timeout")
	// Skip Sync() — on Windows, fsync on pipes deadlocks (FlushFileBuffers).

	_ = stdoutW.Close()
	_ = stderrW.Close()
	stdoutBytes, _ := io.ReadAll(stdoutR)
	stderrBytes, _ := io.ReadAll(stderrR)
	stdoutText := string(stdoutBytes)
	stderrText := string(stderrBytes)

	if !strings.Contains(stdoutText, "service started") {
		t.Fatalf("stdout missing info log: %s", stdoutText)
	}
	if !strings.Contains(stderrText, "Warning: queue full") {
		t.Fatalf("stderr missing warn log: %s", stderrText)
	}
	if !strings.Contains(stderrText, "Forward request failed: timeout") {
		t.Fatalf("stderr missing error log: %s", stderrText)
	}
	if !strings.Contains(stderrText, "\"legacy_stdlog\":true") {
		t.Fatalf("stderr missing legacy_stdlog marker: %s", stderrText)
	}
}

func TestLegacyPrintfRoutesLevels(t *testing.T) {
	origStdout := os.Stdout
	origStderr := os.Stderr
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	os.Stdout = stdoutW
	os.Stderr = stderrW
	t.Cleanup(func() {
		os.Stdout = origStdout
		os.Stderr = origStderr
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		_ = stderrR.Close()
		_ = stderrW.Close()
	})

	if err := Init(InitOptions{
		Level:       "debug",
		Format:      "json",
		ServiceName: "sub2api",
		Environment: "test",
		Output: OutputOptions{
			ToStdout: true,
			ToFile:   false,
		},
		Sampling: SamplingOptions{Enabled: false},
	}); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	LegacyPrintf("service.test", "request started")
	LegacyPrintf("service.test", "Warning: queue full")
	LegacyPrintf("service.test", "forward failed: timeout")
	LegacyPrintf("service.test", "usage_logs 保留清理失败: %s", "statement timeout")
	// Skip Sync() — on Windows, fsync on pipes deadlocks (FlushFileBuffers).

	_ = stdoutW.Close()
	_ = stderrW.Close()
	stdoutBytes, _ := io.ReadAll(stdoutR)
	stderrBytes, _ := io.ReadAll(stderrR)
	stdoutText := string(stdoutBytes)
	stderrText := string(stderrBytes)

	if !strings.Contains(stdoutText, "request started") {
		t.Fatalf("stdout missing info log: %s", stdoutText)
	}
	if strings.Contains(stdoutText, "保留清理失败") {
		t.Fatalf("chinese failure log must not be routed as info: %s", stdoutText)
	}
	if line := findLogLine(stderrText, "usage_logs 保留清理失败: statement timeout"); !strings.Contains(line, "\"level\":\"ERROR\"") {
		t.Fatalf("chinese failure log should be ERROR, got line=%q stderr=%s", line, stderrText)
	}
	if !strings.Contains(stderrText, "Warning: queue full") {
		t.Fatalf("stderr missing warn log: %s", stderrText)
	}
	if !strings.Contains(stderrText, "forward failed: timeout") {
		t.Fatalf("stderr missing error log: %s", stderrText)
	}
	if !strings.Contains(stderrText, "\"legacy_printf\":true") {
		t.Fatalf("stderr missing legacy_printf marker: %s", stderrText)
	}
	if !strings.Contains(stderrText, "\"component\":\"service.test\"") {
		t.Fatalf("stderr missing component field: %s", stderrText)
	}
}

func findLogLine(output, substr string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, substr) {
			return line
		}
	}
	return ""
}

type captureSink struct {
	mu     sync.Mutex
	events []*LogEvent
}

func (s *captureSink) WriteLogEvent(event *LogEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

func (s *captureSink) find(message string) *LogEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, event := range s.events {
		if event != nil && event.Message == message {
			return event
		}
	}
	return nil
}

func TestLegacyPrintfSkipOpsSystemLogMatchesLegacyPrintfAndMarksSkip(t *testing.T) {
	origStdout := os.Stdout
	origStderr := os.Stderr
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	os.Stdout = stdoutW
	os.Stderr = stderrW
	t.Cleanup(func() {
		os.Stdout = origStdout
		os.Stderr = origStderr
		SetSink(nil)
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		_ = stderrR.Close()
		_ = stderrW.Close()
	})
	// 并发读取，避免输出超过管道缓冲时写端阻塞。
	stdoutCh := make(chan string, 1)
	stderrCh := make(chan string, 1)
	go func() { b, _ := io.ReadAll(stdoutR); stdoutCh <- string(b) }()
	go func() { b, _ := io.ReadAll(stderrR); stderrCh <- string(b) }()

	if err := Init(InitOptions{
		Level:       "debug",
		Format:      "json",
		ServiceName: "sub2api",
		Environment: "test",
		Caller:      true,
		Output: OutputOptions{
			ToStdout: true,
			ToFile:   false,
		},
		Sampling: SamplingOptions{Enabled: false},
	}); err != nil {
		t.Fatalf("Init() error: %v", err)
	}
	sink := &captureSink{}
	SetSink(sink)

	cases := []struct {
		msg  string
		want Level
	}{
		{msg: "[Forward] Upstream error (failover): Account=1(a) Status=429", want: LevelError},
		{msg: "Warning: queue full", want: LevelWarn},
		{msg: "request started", want: LevelInfo},
	}
	for _, tc := range cases {
		LegacyPrintf("service.test", "plain %s", tc.msg)
		LegacyPrintfSkipOpsSystemLog("service.test", "skip %s", tc.msg)
	}
	// Skip Sync() — on Windows, fsync on pipes deadlocks (FlushFileBuffers).

	_ = stdoutW.Close()
	_ = stderrW.Close()
	stdoutText := <-stdoutCh
	stderrText := <-stderrCh

	for _, tc := range cases {
		plain := sink.find("plain " + tc.msg)
		skipped := sink.find("skip " + tc.msg)
		if plain == nil || skipped == nil {
			t.Fatalf("sink missing events for %q: plain=%v skipped=%v", tc.msg, plain, skipped)
		}
		wantLevel := strings.ToLower(tc.want.String())
		if plain.Level != wantLevel || skipped.Level != plain.Level {
			t.Fatalf("level mismatch for %q: plain=%s skipped=%s want=%s", tc.msg, plain.Level, skipped.Level, wantLevel)
		}
		if skip, _ := skipped.Fields[OpsSystemLogSkipField].(bool); !skip {
			t.Fatalf("skip variant must carry %s=true, fields=%v", OpsSystemLogSkipField, skipped.Fields)
		}
		if _, ok := plain.Fields[OpsSystemLogSkipField]; ok {
			t.Fatalf("LegacyPrintf must not carry %s, fields=%v", OpsSystemLogSkipField, plain.Fields)
		}
		if skipped.Fields["component"] != "service.test" || skipped.Fields["legacy_printf"] != true {
			t.Fatalf("skip variant must keep component/legacy_printf fields, fields=%v", skipped.Fields)
		}

		output := stdoutText
		if tc.want >= LevelWarn {
			output = stderrText
		}
		line := findLogLine(output, "skip "+tc.msg)
		if line == "" {
			t.Fatalf("skip variant missing from standard output for %q: stdout=%s stderr=%s", tc.msg, stdoutText, stderrText)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(line), &payload); err != nil {
			t.Fatalf("parse log json failed: %v, line=%s", err, line)
		}
		if caller, _ := payload["caller"].(string); !strings.Contains(caller, "stdlog_bridge_test.go:") {
			t.Fatalf("caller should point to the business call site, got: %s", caller)
		}
		if plainLine := findLogLine(output, "plain "+tc.msg); !strings.Contains(plainLine, "stdlog_bridge_test.go:") {
			t.Fatalf("LegacyPrintf caller should point to the business call site, got line: %s", plainLine)
		}
	}
}
