package server

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/reqlog"
)

// 慢请求占比超阈值 → 一条聚合 WARN；窗口内不重复刷。
func TestUpstreamHealthWarnsOncePerWindow(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	u := &upstreamHealth{}
	for i := 0; i < 12; i++ {
		u.record(200, reqlog.OutcomeSuccess, 9*time.Second)
	}
	if n := strings.Count(buf.String(), "upstream degraded"); n != 1 {
		t.Fatalf("want 1 warn, got %d: %s", n, buf.String())
	}
	for i := 0; i < 12; i++ {
		u.record(200, reqlog.OutcomeSuccess, 9*time.Second)
	}
	if n := strings.Count(buf.String(), "upstream degraded"); n != 1 {
		t.Fatalf("限频失效，重复告警 %d 次", n)
	}
}

// 正常速度不告警；499/interrupted 不采样（用户点停止不能触发告警）。
func TestUpstreamHealthIgnoresNormalAndInterrupted(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	u := &upstreamHealth{}
	for i := 0; i < 20; i++ {
		u.record(200, reqlog.OutcomeSuccess, 2*time.Second)
		u.record(499, reqlog.OutcomeInterrupted, 0)
	}
	if strings.Contains(buf.String(), "upstream degraded") {
		t.Fatalf("不应告警: %s", buf.String())
	}
	if got := u.String(); !strings.Contains(got, "samples=20") {
		t.Fatalf("499 不应进样本: %s", got)
	}
}
