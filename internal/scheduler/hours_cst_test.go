package scheduler

import (
	"testing"
	"time"
)

// 回归钉：档位按北京时间判定，与宿主时区无关。
// 旧实现用 now.Location() 构造 time.Date，宿主跑在 UTC 时 [10] 会落到 CST 18:00，
// 签到/活跃/夜猫子整体晚 8 小时——不报错，只静默少拿积分。
func TestNextFireUsesBeijingHoursUnderUTCHost(t *testing.T) {
	old := time.Local
	time.Local = time.UTC
	defer func() { time.Local = old }()

	// UTC 2026-11-06 20:00 = CST 2026-11-07 04:00
	now := time.Date(2026, 11, 6, 20, 0, 0, 0, time.UTC)

	cases := []struct {
		hour int
		want time.Time
	}{
		// CST 11-07 10:00 = UTC 11-07 02:00
		{10, time.Date(2026, 11, 7, 2, 0, 0, 0, time.UTC)},
		// CST 11-07 22:00 = UTC 11-07 14:00
		{22, time.Date(2026, 11, 7, 14, 0, 0, 0, time.UTC)},
		// CST 11-07 03:00 = UTC 11-06 19:00，已过 → 顺延到 CST 11-08 03:00 = UTC 11-07 19:00
		{3, time.Date(2026, 11, 7, 19, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		got := nextFire(now, []int{c.hour})
		if !got.Equal(c.want) {
			t.Errorf("nextFire(%02d:00) = %s want %s（差 %s）",
				c.hour, got.UTC().Format(time.RFC3339), c.want.Format(time.RFC3339), got.Sub(c.want))
		}
	}
}

// 同一条断言在非 UTC 宿主上也要成立：换成本地时区不影响绝对时刻。
func TestNextFireIndependentOfHostZone(t *testing.T) {
	for _, loc := range []*time.Location{time.UTC, time.FixedZone("X", -5*3600), time.FixedZone("Y", 13*3600)} {
		old := time.Local
		time.Local = loc
		now := time.Date(2026, 11, 6, 20, 0, 0, 0, time.UTC)
		got := nextFire(now, []int{10})
		time.Local = old
		want := time.Date(2026, 11, 7, 2, 0, 0, 0, time.UTC)
		if !got.Equal(want) {
			t.Errorf("host=%s: got %s want %s", loc, got.UTC().Format(time.RFC3339), want.Format(time.RFC3339))
		}
	}
}
