package cst

import (
	"testing"
	"time"
)

// 回归钉：宿主跑在 UTC 时，日界必须仍然按北京时间切。
// 每个用例的入参都是 UTC 时刻，期望值都是 CST 下的日键——两者相差 8 小时，
// 一旦有人把 Day 改回 t.Format(...)（本地时区），下面全部会红。
func TestDayUsesBeijingRegardlessOfHostZone(t *testing.T) {
	utc := time.UTC
	cases := []struct {
		name string
		in   time.Time
		want string
	}{
		{"CST 同日 08:00", time.Date(2026, 11, 6, 0, 0, 0, 0, utc), "2026-11-06"},
		{"CST 前一日 23:59", time.Date(2026, 11, 5, 15, 59, 0, 0, utc), "2026-11-05"},
		{"CST 次日 00:01（UTC 仍是前一天）", time.Date(2026, 11, 6, 16, 1, 0, 0, utc), "2026-11-07"},
		{"跨月：UTC 月末 16:30 = CST 次月 00:30", time.Date(2026, 11, 30, 16, 30, 0, 0, utc), "2026-12-01"},
		{"跨年：UTC 12-31 16:30 = CST 次年 01-01", time.Date(2026, 12, 31, 16, 30, 0, 0, utc), "2027-01-01"},
	}
	for _, c := range cases {
		if got := Day(c.in); got != c.want {
			t.Errorf("%s: Day(%s)=%s want %s", c.name, c.in.Format(time.RFC3339), got, c.want)
		}
	}
}

func TestYesterdayCrossesMonth(t *testing.T) {
	// UTC 12-01 00:30 → CST 12-01 08:30，昨天应是 11-30（不是 11-31，也不是 UTC 的 11-30 之外）
	in := time.Date(2026, 12, 1, 0, 30, 0, 0, time.UTC)
	if got := Yesterday(in); got != "2026-11-30" {
		t.Fatalf("Yesterday=%s want 2026-11-30", got)
	}
}

func TestAtIsBeijingHourNotHostHour(t *testing.T) {
	// UTC 2026-11-06 20:00 = CST 2026-11-07 04:00；hour=10 应落在 CST 11-07 10:00 = UTC 02:00
	now := time.Date(2026, 11, 6, 20, 0, 0, 0, time.UTC)
	got := At(now, 10)
	want := time.Date(2026, 11, 7, 2, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("At=%s want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
	if h := got.In(Zone()).Hour(); h != 10 {
		t.Fatalf("At 的 CST 小时=%d want 10", h)
	}
}

func TestZoneIsUTCPlus8(t *testing.T) {
	_, off := time.Date(2026, 1, 1, 0, 0, 0, 0, Zone()).Zone()
	if off != 8*60*60 {
		t.Fatalf("offset=%d want 28800", off)
	}
	if name := Now().Location().String(); name != "CST" {
		t.Fatalf("location=%s want CST", name)
	}
}
