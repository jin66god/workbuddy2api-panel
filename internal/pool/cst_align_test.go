package pool

import (
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// 回归钉：硬冷却「次日 04:00」按北京时间算。
// 旧实现用 now.Hour()/now.Location()，宿主跑 UTC 时会冷到 CST 12:00——签到早已结束，
// 白扔一个号约 8 小时。
func TestNextDay4AMIsBeijingTime(t *testing.T) {
	cases := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{
			// UTC 2026-11-06 18:00 = CST 11-07 02:00（凌晨，当天 04:00 未到）
			// → CST 11-07 04:00 = UTC 11-06 20:00
			name: "CST 凌晨两点→当天 04:00",
			now:  time.Date(2026, 11, 6, 18, 0, 0, 0, time.UTC),
			want: time.Date(2026, 11, 6, 20, 0, 0, 0, time.UTC),
		},
		{
			// UTC 2026-11-06 20:00 = CST 11-07 04:00（已到点）→ 次日 CST 04:00 = UTC 11-07 20:00
			name: "CST 04:00 整→次日",
			now:  time.Date(2026, 11, 6, 20, 0, 0, 0, time.UTC),
			want: time.Date(2026, 11, 7, 20, 0, 0, 0, time.UTC),
		},
		{
			// UTC 2026-11-06 10:00 = CST 18:00 → 次日 CST 04:00 = UTC 11-06 20:00
			name: "CST 傍晚→次日 04:00",
			now:  time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC),
			want: time.Date(2026, 11, 6, 20, 0, 0, 0, time.UTC),
		},
	}
	for _, c := range cases {
		got := nextDay4AM(c.now)
		if !got.Equal(c.want) {
			t.Errorf("%s: got %s want %s（差 %s）", c.name,
				got.UTC().Format(time.RFC3339), c.want.Format(time.RFC3339), got.Sub(c.want))
		}
	}
}

// 签到日键的写入方（NoteCheckinDone）与读取方（Status.CheckinDone）必须同一口径，
// 否则宿主跑 UTC 时会出现「刚签到完，面板/排程仍认为今天没签」。
func TestCheckinDayKeyRoundTripUnderUTCHost(t *testing.T) {
	old := time.Local
	time.Local = time.UTC
	defer func() { time.Local = old }()

	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.NoteCheckinDone("u1")

	st, ok := p.Status("u1")
	if !ok || !st.CheckinDone {
		t.Fatalf("NoteCheckinDone 后 CheckinDone=%v ok=%v：写入方与读取方日键口径不一致", st.CheckinDone, ok)
	}
}
