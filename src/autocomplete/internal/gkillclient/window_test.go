package gkillclient

// 取得を期間で刻む splitIntoWindows のテスト。
//
// 刻み方を間違えると、隙間に落ちた記録が**エラーも出さずに**取得から消える。
// 「隙間が無いこと」を機械で固定しておく。

import (
	"testing"
	"time"
)

func TestSplitIntoWindowsCoversWholeRangeWithoutGap(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	start := now.AddDate(-1, 0, 0)

	windows := splitIntoWindows(&start, nil, 90, now)
	if len(windows) < 2 {
		t.Fatalf("1年を90日で刻めば複数になるはず: %d 個", len(windows))
	}

	// 新しい側から並ぶ
	if !windows[0].end.Equal(now) {
		t.Errorf("最初の窓の終わりが now でない: %v", windows[0].end)
	}
	// 最後は取得開始まで戻る
	last := windows[len(windows)-1]
	if !last.start.Equal(start) {
		t.Errorf("最後の窓の始まりが取得開始でない: got %v, want %v", last.start, start)
	}

	// 隙間が無いこと。窓は新しい順なので、次の窓の終わりが前の窓の始まりと一致する。
	for i := 1; i < len(windows); i++ {
		if !windows[i].end.Equal(windows[i-1].start) {
			t.Errorf("窓 %d と %d の間に隙間がある: %v と %v", i-1, i, windows[i-1].start, windows[i].end)
		}
	}

	// どの窓も終わりが始まりより後
	for i, fetchWindow := range windows {
		if !fetchWindow.end.After(fetchWindow.start) {
			t.Errorf("窓 %d の範囲が壊れている: start=%v end=%v", i, fetchWindow.start, fetchWindow.end)
		}
	}
}

func TestSplitIntoWindowsWithoutStartIsNotSplit(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

	// 開始が無いと範囲が決まらないので刻めない（呼び出し元は従来どおり1回で取る）
	if windows := splitIntoWindows(nil, nil, 90, now); windows != nil {
		t.Errorf("開始が無いのに刻んでいる: %v", windows)
	}
}

func TestSplitIntoWindowsShorterThanWindowIsSingle(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	start := now.AddDate(0, 0, -10)

	windows := splitIntoWindows(&start, nil, 90, now)
	if len(windows) != 1 {
		t.Fatalf("窓幅より短い範囲は1つになるはず: %d 個", len(windows))
	}
	if !windows[0].start.Equal(start) || !windows[0].end.Equal(now) {
		t.Errorf("範囲が一致しない: got start=%v end=%v, want start=%v end=%v",
			windows[0].start, windows[0].end, start, now)
	}
}

func TestSplitIntoWindowsUsesExplicitEnd(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	start := now.AddDate(0, 0, -200)
	end := now.AddDate(0, 0, -100)

	windows := splitIntoWindows(&start, &end, 90, now)
	if len(windows) == 0 {
		t.Fatal("窓が作られていない")
	}
	if !windows[0].end.Equal(end) {
		t.Errorf("終わりに now を使っている（呼び出し元の指定を無視している）: %v", windows[0].end)
	}
	last := windows[len(windows)-1]
	if !last.start.Equal(start) {
		t.Errorf("最後の窓の始まりが取得開始でない: %v", last.start)
	}
}

func TestSplitIntoWindowsInvalidRangeIsEmpty(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	start := now.AddDate(0, 0, 10) // 開始が終わりより後

	if windows := splitIntoWindows(&start, &now, 90, now); len(windows) != 0 {
		t.Errorf("範囲が逆なのに窓を作っている: %v", windows)
	}
}
