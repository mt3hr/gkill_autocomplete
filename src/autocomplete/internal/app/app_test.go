package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/config"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/gkillclient"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/llm"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/store"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/suggest"
)

func at(day int, hour int, minute int) time.Time {
	return time.Date(2020, 6, day, hour, minute, 0, 0, time.UTC)
}

func recordAt(id string, when time.Time, tags ...string) suggest.Record {
	return suggest.Record{ID: id, DataType: "kmemo", RelatedTime: when, Tags: tags}
}

func TestNeighborsOfFindsNearbyRecordsOnly(t *testing.T) {
	records := []suggest.Record{
		recordAt("far-before", at(1, 12, 0)),
		recordAt("just-before", at(1, 12, 58)),
		recordAt("target", at(1, 13, 0)),
		recordAt("just-after", at(1, 13, 2)),
		recordAt("far-after", at(1, 14, 0)),
	}
	target := records[2]

	neighbors := neighborsOf(records, target, 5*time.Minute)

	if len(neighbors) != 2 {
		t.Fatalf("近傍 = %d件, want 2件: %+v", len(neighbors), neighbors)
	}
	for _, neighbor := range neighbors {
		if neighbor.ID == "target" {
			t.Error("自分自身が近傍に入っている")
		}
	}
}

func TestNeighborsOfWithZeroWindow(t *testing.T) {
	records := []suggest.Record{recordAt("a", at(1, 13, 0)), recordAt("b", at(1, 13, 1))}
	if neighbors := neighborsOf(records, records[0], 0); len(neighbors) != 0 {
		t.Errorf("窓が0なのに近傍を返した: %+v", neighbors)
	}
}

func TestProfileContextsDetectsMachineTagging(t *testing.T) {
	// 1種類のタグがほぼ全件に付いていて、タグ無しもほとんど無い文脈は、
	// 機械が一律に付けている。人が選ぶ余地が無いので提案の対象にしない。
	records := []suggest.Record{}
	for i := range 100 {
		records = append(records, recordAt("auto", at(1, i%24, i%60), "自動付与タグ"))
	}

	profiles := ProfileContexts(records)

	if len(profiles) != 1 {
		t.Fatalf("文脈 = %d件, want 1件", len(profiles))
	}
	if !profiles[0].LooksMachineTagged() {
		t.Errorf("機械付与と判定されなかった: %+v", profiles[0])
	}
	if profiles[0].LooksHandTagged() {
		t.Error("機械付与の文脈が手作業と判定された")
	}
}

func TestProfileContextsDetectsHandTagging(t *testing.T) {
	// 複数のタグを使い分けていて、タグを付けない記録も混じっている文脈は、
	// 人が選んでいる。ここが提案の値打ちがある場所。
	records := []suggest.Record{}
	for i := range 12 {
		records = append(records, recordAt("a", at(1, i, 0), "タグA"))
	}
	for i := range 10 {
		records = append(records, recordAt("b", at(2, i, 0), "タグB"))
	}
	for i := range 8 {
		records = append(records, recordAt("c", at(3, i, 0)))
	}

	profiles := ProfileContexts(records)

	if !profiles[0].LooksHandTagged() {
		t.Errorf("手作業と判定されなかった: %+v", profiles[0])
	}
	if profiles[0].DistinctTags() != 2 {
		t.Errorf("タグの種類 = %d, want 2", profiles[0].DistinctTags())
	}
	if got := profiles[0].UntaggedRate(); got < 0.25 || got > 0.28 {
		t.Errorf("タグ無しの割合 = %v, want 約0.267", got)
	}
}

func TestProfileContextsIgnoresTooSmallContext(t *testing.T) {
	// 判断材料が足りない文脈で決め打ちしない。
	records := []suggest.Record{
		recordAt("a", at(1, 0, 0), "タグA"),
		recordAt("b", at(1, 1, 0), "タグB"),
	}

	profiles := ProfileContexts(records)

	if profiles[0].LooksHandTagged() {
		t.Errorf("記録が少ないのに対象と判定された: %+v", profiles[0])
	}
}

// newAnalyzeTestServer は gkill を模したサーバを立てる。
func newAnalyzeTestServer(t *testing.T, kyous []map[string]any) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/login":
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": nil, "session_id": "session-1"})
		case "/api/get_kyous_mcp":
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": nil, "kyous": kyous, "has_more": false})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": nil})
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// testUserID は解析を回す利用者。保存先の行はすべてこの人のものになる。
const testUserID = "testuser"

// fixedSessionSource は gkillauth.SessionProvider の代わり。
//
// 本物は gkill の設定ディレクトリへ直接書くので、テストでは固定値を返す。
type fixedSessionSource struct{}

func (fixedSessionSource) UserID() string { return testUserID }

func (fixedSessionSource) SessionID(_ context.Context) (string, error) { return "session-1", nil }

func (fixedSessionSource) Invalidate(_ context.Context) {}

func newTestApp(t *testing.T, server *httptest.Server, appConfig config.Config) *App {
	t.Helper()

	appConfig.Gkill.BaseURL = server.URL

	home := t.TempDir()
	client, err := gkillclient.New(appConfig.Gkill, server.URL, fixedSessionSource{})
	if err != nil {
		t.Fatalf("クライアントを作れない: %v", err)
	}
	openedStore, err := store.Open(context.Background(), filepath.Join(home, "test.db"))
	if err != nil {
		t.Fatalf("保存先を開けない: %v", err)
	}
	t.Cleanup(func() { _ = openedStore.Close() })

	return &App{
		Config: appConfig,
		Client: client,
		Store:  openedStore,
		Logger: slog.New(slog.DiscardHandler),
		Now:    func() time.Time { return at(10, 12, 0) },
	}
}

// kyouJSON は gkill のレスポンス1件ぶんを組み立てる。
func kyouJSON(id string, when time.Time, tags []string, text string) map[string]any {
	entry := map[string]any{
		"id":           id,
		"data_type":    "kmemo",
		"related_time": when.Format(time.RFC3339),
		"payload":      map[string]any{"kind": "kmemo", "content": text},
	}
	if len(tags) > 0 {
		entry["tags"] = tags
	}
	return entry
}

func TestAnalyzeSuggestsFromRepeatedText(t *testing.T) {
	// 同じ文面の記録に同じタグを付けてきた履歴があれば、
	// LLM を使わずに提案できる。
	kyous := []map[string]any{
		kyouJSON("past-1", at(5, 8, 0), []string{"タグA"}, "定型の本文"),
		kyouJSON("past-2", at(6, 9, 0), []string{"タグA"}, "定型の本文"),
		kyouJSON("new-1", at(9, 10, 0), nil, "定型の本文"),
	}

	application := newTestApp(t, newAnalyzeTestServer(t, kyous), config.Default())

	report, err := application.Analyze(context.Background())
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}

	if report.CandidateRecords != 1 {
		t.Errorf("判定対象 = %d件, want 1件", report.CandidateRecords)
	}
	if report.StoredSuggestions != 1 {
		t.Errorf("保存した提案 = %d件, want 1件", report.StoredSuggestions)
	}

	pending, err := application.Store.ListPending(context.Background(), testUserID)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(pending) != 1 || pending[0].Tag != "タグA" || pending[0].TargetID != "new-1" {
		t.Errorf("提案 = %+v", pending)
	}
}

// failingClassifier は必ず失敗する判定器。
//
// LLM の時間切れ・応答の解釈失敗・写真が取れない、といったことを模す。
type failingClassifier struct {
	calls int
}

func (c *failingClassifier) Classify(_ context.Context, _ suggest.Record, _ []suggest.Candidate) ([]suggest.Judgement, error) {
	c.calls++
	return nil, errors.New("判定に失敗しました")
}

// TestAnalyzeDoesNotStopAtFirstFailure は、判定に失敗した記録があっても
// 残りの記録が捨てられないことを確かめる。
//
// **これを落とすと「解析が数件で止まる」に戻る。** 1件の失敗で解析全体を
// 諦めていたころは、後ろに並んだ記録が永久に処理されなかった。
// しかも失敗する記録は評価済みにならないので、何度やり直しても
// 同じ場所で止まり続けた。
func TestAnalyzeDoesNotStopAtFirstFailure(t *testing.T) {
	kyous := []map[string]any{
		// 候補タグが立つだけの履歴。
		kyouJSON("past-1", at(5, 8, 0), []string{"タグA"}, "むかしの本文"),
		// 判定対象。本文はどれも履歴と一致せず、タグ名も含まない。
		kyouJSON("new-1", at(9, 10, 0), nil, "ひとつめ"),
		kyouJSON("new-2", at(9, 12, 0), nil, "ふたつめ"),
		kyouJSON("new-3", at(9, 14, 0), nil, "みっつめ"),
	}

	appConfig := config.Default()
	// 履歴1件でも候補タグが立つようにする。
	appConfig.Candidates.MinExamples = 1

	application := newTestApp(t, newAnalyzeTestServer(t, kyous), appConfig)
	classifier := &failingClassifier{}
	application.Classifier = classifier

	report, err := application.Analyze(context.Background())
	if err != nil {
		t.Fatalf("1件の失敗で解析全体が落ちている: %v", err)
	}

	if report.CandidateRecords != 3 {
		t.Fatalf("判定対象 = %d件, want 3件", report.CandidateRecords)
	}
	if classifier.calls != 3 {
		t.Errorf("判定器の呼び出し = %d回, want 3回 (失敗しても次へ進むはず)", classifier.calls)
	}
	if report.FailedRecords != 3 {
		t.Errorf("判定に失敗した記録 = %d件, want 3件", report.FailedRecords)
	}

	// 失敗した記録は評価済みにしない。次の解析でやり直せるようにするため。
	evaluated, err := application.Store.EvaluatedTargetIDs(context.Background(), testUserID)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(evaluated) != 0 {
		t.Errorf("評価済みの記録 = %d件, want 0件 (失敗した記録は次にやり直す)", len(evaluated))
	}
}

// TestAnalyzeReportsWhyEveryRecordFailed は、全件失敗したときに理由が残ることを確かめる。
//
// **件数だけでは何をすればよいか分からない。** LLM を落としたまま解析すると
// 全件が失敗するが、飛ばして進む作りでは「N件中N件失敗」としか出ず、
// 原因に辿り着けない。理由は決め打ちの文字列で、記録の中身を含まない。
func TestAnalyzeReportsWhyEveryRecordFailed(t *testing.T) {
	kyous := []map[string]any{
		kyouJSON("past-1", at(5, 8, 0), []string{"タグA"}, "むかしの本文"),
		kyouJSON("new-1", at(9, 10, 0), nil, "ひとつめ"),
		kyouJSON("new-2", at(9, 12, 0), nil, "ふたつめ"),
	}

	appConfig := config.Default()
	appConfig.Candidates.MinExamples = 1

	application := newTestApp(t, newAnalyzeTestServer(t, kyous), appConfig)
	// LLM が起動していない状況を模す。
	application.Classifier = &unreachableClassifier{}

	report, err := application.Analyze(context.Background())
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}

	if report.FailedRecords != report.CandidateRecords || report.CandidateRecords == 0 {
		t.Fatalf("失敗 %d件 / 対象 %d件, want 全件失敗", report.FailedRecords, report.CandidateRecords)
	}
	if !strings.Contains(report.FailureReason, "LLM に繋がらない") {
		t.Errorf("理由 = %q, want 「LLM に繋がらない」を含む", report.FailureReason)
	}
	// 理由は決め打ちの文字列だけ。記録の中身を混ぜない。
	for _, secret := range []string{"ひとつめ", "ふたつめ", "むかしの本文"} {
		if strings.Contains(report.FailureReason, secret) {
			t.Errorf("理由に記録の中身が入っている: %q", report.FailureReason)
		}
	}
}

// unreachableClassifier は LLM に繋がらない状況を模す判定器。
type unreachableClassifier struct{}

func (unreachableClassifier) Classify(_ context.Context, _ suggest.Record, _ []suggest.Candidate) ([]suggest.Judgement, error) {
	return nil, fmt.Errorf("%w: dial tcp 127.0.0.1:11434: connection refused", llm.ErrUnreachable)
}

// TestAnalyzeStopsWhenCancelled は、中断は中断として扱うことを確かめる。
//
// 判定の失敗は飛ばして進むが、利用者が止めたときまで進み続けてはいけない。
func TestAnalyzeStopsWhenCancelled(t *testing.T) {
	kyous := []map[string]any{
		kyouJSON("past-1", at(5, 8, 0), []string{"タグA"}, "むかしの本文"),
		kyouJSON("new-1", at(9, 10, 0), nil, "ひとつめ"),
		kyouJSON("new-2", at(9, 12, 0), nil, "ふたつめ"),
	}

	appConfig := config.Default()
	appConfig.Candidates.MinExamples = 1

	application := newTestApp(t, newAnalyzeTestServer(t, kyous), appConfig)

	ctx, cancel := context.WithCancel(context.Background())
	// 1件目の判定に入ったところで中断する。
	application.Classifier = &cancellingClassifier{cancel: cancel}

	if _, err := application.Analyze(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("エラー = %v, want context.Canceled", err)
	}
}

// cancellingClassifier は呼ばれた時点で解析を中断させる判定器。
type cancellingClassifier struct {
	cancel context.CancelFunc
}

func (c *cancellingClassifier) Classify(_ context.Context, _ suggest.Record, _ []suggest.Candidate) ([]suggest.Judgement, error) {
	c.cancel()
	return nil, errors.New("中断されました")
}

func TestAnalyzeSkipsAlreadyTaggedRecords(t *testing.T) {
	kyous := []map[string]any{
		kyouJSON("past-1", at(5, 8, 0), []string{"タグA"}, "定型の本文"),
		kyouJSON("already", at(9, 10, 0), []string{"タグB"}, "定型の本文"),
	}

	application := newTestApp(t, newAnalyzeTestServer(t, kyous), config.Default())

	report, err := application.Analyze(context.Background())
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if report.CandidateRecords != 0 {
		t.Errorf("既にタグの付いた記録を対象にしている: %d件", report.CandidateRecords)
	}
}

func TestAnalyzeDoesNotResurrectRejectedSuggestions(t *testing.T) {
	// 却下したものが次の解析で復活しないこと。
	kyous := []map[string]any{
		kyouJSON("past-1", at(5, 8, 0), []string{"タグA"}, "定型の本文"),
		kyouJSON("new-1", at(9, 10, 0), nil, "定型の本文"),
	}

	application := newTestApp(t, newAnalyzeTestServer(t, kyous), config.Default())
	ctx := context.Background()

	if _, err := application.Analyze(ctx); err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	pending, err := application.Store.ListPending(ctx, testUserID)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("提案 = %d件, want 1件", len(pending))
	}

	if err := application.Store.Decide(ctx, testUserID, pending[0].ID, store.DecisionRejected, at(10, 13, 0)); err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}

	// 判定済みの印を消して、解析をやり直せる状態にする。
	if err := application.Store.ClearSuggestions(ctx, testUserID); err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}

	if _, err := application.Analyze(ctx); err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	afterRerun, err := application.Store.ListPending(ctx, testUserID)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(afterRerun) != 0 {
		t.Errorf("却下したはずの提案が復活した: %+v", afterRerun)
	}
}

func TestAnalyzeSkipsAlreadyEvaluatedRecords(t *testing.T) {
	// 同じ記録を二度判定しない。LLM を使う段階では往復の無駄が大きい。
	kyous := []map[string]any{
		kyouJSON("past-1", at(5, 8, 0), []string{"タグA"}, "定型の本文"),
		kyouJSON("new-1", at(9, 10, 0), nil, "見たことのない本文"),
	}

	application := newTestApp(t, newAnalyzeTestServer(t, kyous), config.Default())
	ctx := context.Background()

	first, err := application.Analyze(ctx)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if first.CandidateRecords != 1 {
		t.Fatalf("1回目の判定対象 = %d件, want 1件", first.CandidateRecords)
	}
	// 提案は0件になるが、それは正常な結果。
	if first.NoSuggestionRecords != 1 {
		t.Errorf("提案なし = %d件, want 1件", first.NoSuggestionRecords)
	}

	second, err := application.Analyze(ctx)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if second.CandidateRecords != 0 {
		t.Errorf("2回目も同じ記録を判定している: %d件", second.CandidateRecords)
	}
}

func TestAnalyzeIgnoresRecordsOlderThanCandidateWindow(t *testing.T) {
	appConfig := config.Default()
	appConfig.Scope.CandidateDays = 3
	appConfig.Scope.LearnDays = 180

	kyous := []map[string]any{
		kyouJSON("past-1", at(1, 8, 0), []string{"タグA"}, "定型の本文"),
		// 判定の基準時刻は 6/10 12:00。候補範囲は3日なので 6/7 より前は対象外。
		kyouJSON("too-old", at(5, 10, 0), nil, "定型の本文"),
		kyouJSON("recent", at(9, 10, 0), nil, "定型の本文"),
	}

	application := newTestApp(t, newAnalyzeTestServer(t, kyous), appConfig)

	report, err := application.Analyze(context.Background())
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if report.CandidateRecords != 1 {
		t.Errorf("判定対象 = %d件, want 1件 (古い記録が混ざっている)", report.CandidateRecords)
	}
	// 学習には古い記録も使う。
	if report.LearnedRecords != 3 {
		t.Errorf("学習した記録 = %d件, want 3件", report.LearnedRecords)
	}
}

func TestAnalyzeWarnsWhenScanIsTruncated(t *testing.T) {
	// 取得が上限で止まると学習が不完全なまま「正常終了」に見える。
	// 黙って質を落とさないよう、達したことを知らせること。
	logged := &strings.Builder{}

	appConfig := config.Default()
	appConfig.Scope.MaxScanRecords = 2

	kyous := []map[string]any{
		kyouJSON("a", at(5, 8, 0), []string{"タグA"}, "本文1"),
		kyouJSON("b", at(6, 8, 0), []string{"タグA"}, "本文2"),
		kyouJSON("c", at(7, 8, 0), []string{"タグA"}, "本文3"),
	}

	application := newTestApp(t, newAnalyzeTestServer(t, kyous), appConfig)
	application.Logger = slog.New(slog.NewTextHandler(logged, &slog.HandlerOptions{Level: slog.LevelWarn}))

	if _, err := application.Analyze(context.Background()); err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}

	message := logged.String()
	if !strings.Contains(message, "上限") {
		t.Errorf("打ち切りの警告が出ていない: %q", message)
	}
	// 何をすればよいかまで書くこと。
	for _, want := range []string{"rep_prefixes", "learn_days", "max_scan_records"} {
		if !strings.Contains(message, want) {
			t.Errorf("%q が警告に含まれていない: %q", want, message)
		}
	}
}

func TestAnalyzeDoesNotWarnWhenScanFits(t *testing.T) {
	logged := &strings.Builder{}

	appConfig := config.Default()
	appConfig.Scope.MaxScanRecords = 100

	kyous := []map[string]any{
		kyouJSON("a", at(5, 8, 0), []string{"タグA"}, "本文1"),
	}

	application := newTestApp(t, newAnalyzeTestServer(t, kyous), appConfig)
	application.Logger = slog.New(slog.NewTextHandler(logged, &slog.HandlerOptions{Level: slog.LevelWarn}))

	if _, err := application.Analyze(context.Background()); err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}

	if strings.Contains(logged.String(), "上限") {
		t.Errorf("上限に達していないのに警告が出た: %q", logged.String())
	}
}

func TestAnalyzeExcludesMachineAppliedTags(t *testing.T) {
	appConfig := config.Default()
	appConfig.Exclude.TagPatterns = []string{"example_auto_*"}

	kyous := []map[string]any{
		kyouJSON("past-1", at(5, 8, 0), []string{"example_auto_media"}, "定型の本文"),
		kyouJSON("past-2", at(6, 8, 0), []string{"example_auto_media"}, "定型の本文"),
		kyouJSON("new-1", at(9, 10, 0), nil, "定型の本文"),
	}

	application := newTestApp(t, newAnalyzeTestServer(t, kyous), appConfig)

	report, err := application.Analyze(context.Background())
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if report.StoredSuggestions != 0 {
		t.Errorf("除外したはずのタグを提案している: %d件", report.StoredSuggestions)
	}
}
