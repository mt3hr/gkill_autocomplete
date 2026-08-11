package app

import (
	"context"
	"testing"
	"time"

	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/config"
)

func day(day int) time.Time {
	return time.Date(2020, 6, day, 12, 0, 0, 0, time.UTC)
}

func TestBenchmarkDoesNotLearnFromEvaluationPeriod(t *testing.T) {
	// 計測期間の記録を学習に混ぜると、逐語一致が自分自身を引き当てて
	// 満点が出てしまう。学習は計測期間より前だけに限ること。
	kyous := []map[string]any{
		// 計測期間の中にしかない文面。学習が正しく分離されていれば当てられない。
		kyouJSON("eval-1", day(20), []string{"タグA"}, "計測期間にしかない本文"),
		kyouJSON("eval-2", day(21), []string{"タグA"}, "計測期間にしかない本文"),
	}

	application := newTestApp(t, newAnalyzeTestServer(t, kyous), config.Default())

	report, err := application.Benchmark(context.Background(), BenchmarkOptions{
		From: day(15),
		To:   day(25),
	})
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}

	if report.LearnedRecords != 0 {
		t.Errorf("学習した記録 = %d件, want 0件 (計測期間が学習に混ざっている)", report.LearnedRecords)
	}
	if report.EvaluatedRecords != 2 {
		t.Errorf("計測した記録 = %d件, want 2件", report.EvaluatedRecords)
	}
	if report.TruePositive != 0 {
		t.Errorf("当たり = %d件。学習していないのに当てている(答えが漏れている)", report.TruePositive)
	}
	if report.FalseNegative != 2 {
		t.Errorf("取りこぼし = %d件, want 2件", report.FalseNegative)
	}
}

func TestBenchmarkScoresCorrectSuggestion(t *testing.T) {
	kyous := []map[string]any{
		// 学習用(計測期間より前)。
		kyouJSON("past-1", day(1), []string{"タグA"}, "定型の本文"),
		kyouJSON("past-2", day(2), []string{"タグA"}, "定型の本文"),
		// 計測対象。同じ文面なので当てられるはず。
		kyouJSON("eval-1", day(20), []string{"タグA"}, "定型の本文"),
	}

	application := newTestApp(t, newAnalyzeTestServer(t, kyous), config.Default())

	report, err := application.Benchmark(context.Background(), BenchmarkOptions{
		From: day(15),
		To:   day(25),
	})
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}

	if report.LearnedRecords != 2 {
		t.Errorf("学習した記録 = %d件, want 2件", report.LearnedRecords)
	}
	if report.TruePositive != 1 || report.FalsePositive != 0 || report.FalseNegative != 0 {
		t.Errorf("当たり%d 外れ%d 取りこぼし%d, want 1/0/0", report.TruePositive, report.FalsePositive, report.FalseNegative)
	}
	if report.ExactMatchRecords != 1 {
		t.Errorf("完全一致 = %d件, want 1件", report.ExactMatchRecords)
	}
	if report.Precision() != 1 || report.Recall() != 1 {
		t.Errorf("適合率 %v 再現率 %v, want 1/1", report.Precision(), report.Recall())
	}
}

func TestBenchmarkCountsUntaggedRecordsAsAnswers(t *testing.T) {
	// タグを付けなかった記録も正解に含める。
	// 外すと「提案しないのが正解」のケースが評価から消え、
	// 実力を過大に見積もることになる。
	kyous := []map[string]any{
		kyouJSON("past-1", day(1), []string{"タグA"}, "定型の本文"),
		kyouJSON("past-2", day(2), []string{"タグA"}, "定型の本文"),
		// もともとタグを付けなかった記録。何も提案しないのが正解。
		kyouJSON("eval-silent", day(20), nil, "まったく別の本文"),
	}

	application := newTestApp(t, newAnalyzeTestServer(t, kyous), config.Default())

	report, err := application.Benchmark(context.Background(), BenchmarkOptions{
		From: day(15),
		To:   day(25),
	})
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}

	if report.UntaggedRecords != 1 {
		t.Errorf("もともとタグなし = %d件, want 1件", report.UntaggedRecords)
	}
	if report.CorrectlySilentRecords != 1 {
		t.Errorf("正しく黙った = %d件, want 1件", report.CorrectlySilentRecords)
	}
	if report.WronglyNoisyRecords != 0 {
		t.Errorf("余計な提案 = %d件, want 0件", report.WronglyNoisyRecords)
	}
	// 黙るのが正解だったので完全一致にも数える。
	if report.ExactMatchRecords != 1 {
		t.Errorf("完全一致 = %d件, want 1件", report.ExactMatchRecords)
	}
}

func TestBenchmarkCountsWrongSuggestionOnUntaggedRecord(t *testing.T) {
	kyous := []map[string]any{
		kyouJSON("past-1", day(1), []string{"タグA"}, "定型の本文"),
		kyouJSON("past-2", day(2), []string{"タグA"}, "定型の本文"),
		// 本文は同じだが、実際にはタグを付けなかった記録。
		kyouJSON("eval-noisy", day(20), nil, "定型の本文"),
	}

	application := newTestApp(t, newAnalyzeTestServer(t, kyous), config.Default())

	report, err := application.Benchmark(context.Background(), BenchmarkOptions{
		From: day(15),
		To:   day(25),
	})
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}

	if report.WronglyNoisyRecords != 1 {
		t.Errorf("余計な提案 = %d件, want 1件", report.WronglyNoisyRecords)
	}
	if report.FalsePositive != 1 {
		t.Errorf("外れ = %d件, want 1件", report.FalsePositive)
	}
}

func TestBenchmarkWritesNothing(t *testing.T) {
	// 精度計測は読み取りだけ。保存先にも gkill にも書き込まない。
	kyous := []map[string]any{
		kyouJSON("past-1", day(1), []string{"タグA"}, "定型の本文"),
		kyouJSON("past-2", day(2), []string{"タグA"}, "定型の本文"),
		kyouJSON("eval-1", day(20), []string{"タグA"}, "定型の本文"),
	}

	application := newTestApp(t, newAnalyzeTestServer(t, kyous), config.Default())
	ctx := context.Background()

	if _, err := application.Benchmark(ctx, BenchmarkOptions{From: day(15), To: day(25)}); err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}

	pending, err := application.Store.CountPending(ctx, testUserID)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if pending != 0 {
		t.Errorf("提案が保存された: %d件", pending)
	}

	decided, err := application.Store.DecidedTargetIDs(ctx, testUserID)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(decided) != 0 {
		t.Errorf("判定が保存された: %v", decided)
	}

	evaluated, err := application.Store.EvaluatedTargetIDs(ctx, testUserID)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(evaluated) != 0 {
		t.Errorf("判定済みの印が保存された: %v", evaluated)
	}
}

func TestBenchmarkSkipsMachineTaggedRecords(t *testing.T) {
	// 機械が付けたタグしか無い記録は、実運用ではタグ付きなので候補にならない。
	// 混ぜると起こらない母集団を測ることになり、しかも1件ずつ LLM に回って
	// 計測が何時間もかかる。
	appConfig := config.Default()
	appConfig.Exclude.TagPatterns = []string{"example_auto_*"}

	kyous := []map[string]any{
		kyouJSON("past-1", day(1), []string{"タグA"}, "定型の本文"),
		kyouJSON("past-2", day(2), []string{"タグA"}, "定型の本文"),
		// 機械付与のみ → 評価から外す。
		kyouJSON("machine", day(20), []string{"example_auto_media"}, "自動収集の本文"),
		// 人のタグあり → 評価する。
		kyouJSON("hand", day(20), []string{"タグA"}, "定型の本文"),
		// タグ無し → 評価する(「黙るのが正解」の検証)。
		kyouJSON("silent", day(20), nil, "まったく別の本文"),
	}

	application := newTestApp(t, newAnalyzeTestServer(t, kyous), appConfig)

	report, err := application.Benchmark(context.Background(), BenchmarkOptions{From: day(15), To: day(25)})
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}

	if report.SkippedRecords != 1 {
		t.Errorf("評価から外した記録 = %d件, want 1件", report.SkippedRecords)
	}
	if report.TaggedRecords != 1 {
		t.Errorf("タグありの評価 = %d件, want 1件", report.TaggedRecords)
	}
	if report.UntaggedRecords != 1 {
		t.Errorf("タグなしの評価 = %d件, want 1件", report.UntaggedRecords)
	}
	// 機械付与の記録が外れの数に混ざっていないこと。
	if report.FalsePositive != 0 {
		t.Errorf("外れ = %d件, want 0件", report.FalsePositive)
	}
}

func TestBenchmarkKeepsMachineTaggedRecordsWhenAlreadyTaggedIsOff(t *testing.T) {
	// already_tagged を切っている場合、タグ付きも候補になるので評価に残す。
	appConfig := config.Default()
	appConfig.Exclude.AlreadyTagged = false
	appConfig.Exclude.TagPatterns = []string{"example_auto_*"}

	kyous := []map[string]any{
		kyouJSON("past-1", day(1), []string{"タグA"}, "定型の本文"),
		kyouJSON("machine", day(20), []string{"example_auto_media"}, "自動収集の本文"),
	}

	application := newTestApp(t, newAnalyzeTestServer(t, kyous), appConfig)

	report, err := application.Benchmark(context.Background(), BenchmarkOptions{From: day(15), To: day(25)})
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if report.SkippedRecords != 0 {
		t.Errorf("評価から外した記録 = %d件, want 0件", report.SkippedRecords)
	}
}

func TestBenchmarkRejectsInvalidPeriod(t *testing.T) {
	application := newTestApp(t, newAnalyzeTestServer(t, nil), config.Default())

	if _, err := application.Benchmark(context.Background(), BenchmarkOptions{From: day(25), To: day(15)}); err == nil {
		t.Fatal("逆順の期間が通ってしまった")
	}
}
