package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/ids"
)

// testUserID はテストで使う利用者ID。
//
// 保存先はすべての行を利用者で分けるので、テストも必ず誰かとして書く。
const testUserID = "testuser"

// otherUserID は「他人」。分離の検査に使う。
const otherUserID = "otheruser"

func newTestStore(t *testing.T) *Store {
	t.Helper()

	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("保存先を開けない: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// newSuggestion はテスト用の提案を作る。IDは本番と同じ導き方をする。
func newSuggestion(targetID string, tagName string) Suggestion {
	return Suggestion{
		ID:          ids.SuggestionID(targetID, tagName),
		TagID:       ids.TagID(targetID, tagName),
		TargetID:    targetID,
		Tag:         tagName,
		Confidence:  0.8,
		Tier:        "text_match",
		Reason:      "過去の記録と一致",
		RepName:     "SampleRep",
		DataType:    "kmemo",
		RelatedTime: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC),
		SuggestedAt: time.Date(2020, 1, 2, 9, 0, 0, 0, time.UTC),
	}
}

func TestPutAndListPending(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	stored, err := store.PutSuggestion(ctx, testUserID,newSuggestion("target-1", "タグA"))
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if !stored {
		t.Fatal("保存されなかった")
	}

	pending, err := store.ListPending(ctx, testUserID)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("未判定 = %d件, want 1件", len(pending))
	}
	if pending[0].Tag != "タグA" || pending[0].TargetID != "target-1" {
		t.Errorf("中身が違う: %+v", pending[0])
	}
	if !pending[0].RelatedTime.Equal(time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Errorf("related_time が往復で壊れた: %v", pending[0].RelatedTime)
	}
}

func TestPutSuggestionIsIdempotent(t *testing.T) {
	// 同じ (対象, タグ) は決定的に同じIDになるので、再解析しても重複しない。
	store := newTestStore(t)
	ctx := context.Background()

	for range 3 {
		if _, err := store.PutSuggestion(ctx, testUserID,newSuggestion("target-1", "タグA")); err != nil {
			t.Fatalf("想定外のエラー: %v", err)
		}
	}

	pending, err := store.ListPending(ctx, testUserID)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(pending) != 1 {
		t.Errorf("未判定 = %d件, want 1件 (再解析で重複した)", len(pending))
	}
}

func TestRejectedSuggestionNeverComesBack(t *testing.T) {
	// このアプリの根幹。却下したものが解析のたびに復活すると使い物にならない。
	store := newTestStore(t)
	ctx := context.Background()

	suggestion := newSuggestion("target-1", "タグA")
	if _, err := store.PutSuggestion(ctx, testUserID,suggestion); err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if err := store.Decide(ctx, testUserID,suggestion.ID, DecisionRejected, time.Now()); err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}

	// 解析をやり直したつもりで、同じ提案をもう一度入れる。
	stored, err := store.PutSuggestion(ctx, testUserID,suggestion)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if stored {
		t.Error("却下済みの提案が保存された")
	}

	pending, err := store.ListPending(ctx, testUserID)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("却下したはずの提案が未判定に出ている: %+v", pending)
	}
}

func TestApprovedSuggestionDoesNotComeBack(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	suggestion := newSuggestion("target-1", "タグA")
	if _, err := store.PutSuggestion(ctx, testUserID,suggestion); err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if err := store.Decide(ctx, testUserID,suggestion.ID, DecisionApproved, time.Now()); err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}

	pending, err := store.ListPending(ctx, testUserID)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("承認済みの提案が未判定に残っている: %+v", pending)
	}
}

func TestMarkNoTagNeededSilencesWholeRecord(t *testing.T) {
	// 記録の一定割合は意図的にタグを付けないまま残る。
	// それを覚えておかないと毎回蒸し返すことになる。
	store := newTestStore(t)
	ctx := context.Background()

	for _, tagName := range []string{"タグA", "タグB"} {
		if _, err := store.PutSuggestion(ctx, testUserID,newSuggestion("target-1", tagName)); err != nil {
			t.Fatalf("想定外のエラー: %v", err)
		}
	}

	if err := store.MarkNoTagNeeded(ctx, testUserID,"target-1", time.Now()); err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}

	pending, err := store.ListPending(ctx, testUserID)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("タグ不要にした記録の提案が残っている: %+v", pending)
	}

	// 解析をやり直しても、新しいタグの提案すら出さない。
	stored, err := store.PutSuggestion(ctx, testUserID,newSuggestion("target-1", "タグC"))
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if stored {
		t.Error("タグ不要にした記録に新しい提案が保存された")
	}
}

func TestClearSuggestionsKeepsVerdicts(t *testing.T) {
	// 派生データだけを捨てる操作。人間の判定まで消すと、
	// 却下したはずの提案が次の解析で全部復活する。
	store := newTestStore(t)
	ctx := context.Background()

	rejected := newSuggestion("target-1", "タグA")
	if _, err := store.PutSuggestion(ctx, testUserID,rejected); err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if err := store.Decide(ctx, testUserID,rejected.ID, DecisionRejected, time.Now()); err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if err := store.MarkNoTagNeeded(ctx, testUserID,"target-2", time.Now()); err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if err := store.MarkEvaluated(ctx, testUserID,"target-3", "llm", time.Now()); err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}

	if err := store.ClearSuggestions(ctx, testUserID); err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}

	// 判定は残っていること。
	decided, err := store.DecidedTargetIDs(ctx, testUserID)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if _, ok := decided["target-1"]; !ok {
		t.Error("却下の記録が消えた")
	}
	if _, ok := decided["target-2"]; !ok {
		t.Error("タグ不要の記録が消えた")
	}

	// 判定済みの印(派生データ)は消えていること。
	evaluated, err := store.EvaluatedTargetIDs(ctx, testUserID)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(evaluated) != 0 {
		t.Errorf("判定済みの印が残っている: %v", evaluated)
	}

	// 掃除のあとでも却下は効いていること。
	stored, err := store.PutSuggestion(ctx, testUserID,rejected)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if stored {
		t.Error("掃除のあとに却下済みの提案が復活した")
	}
}

func TestDecideUnknownSuggestion(t *testing.T) {
	store := newTestStore(t)

	err := store.Decide(context.Background(), testUserID, "does-not-exist", DecisionApproved, time.Now())
	if err == nil {
		t.Fatal("存在しない提案の判定が通ってしまった")
	}
}

// 利用者をまたいで中身が漏れないこと。
//
// gkill ではアカウントごとに別のリポジトリを持つので、混ざると
// 「他人の記録の本文」が画面に出る。**このアプリで一番まずい壊れ方**なので、
// 読み取り系のすべてで検査する。
func TestSuggestionsAreIsolatedPerUser(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if _, err := store.PutSuggestion(ctx, testUserID, newSuggestion("target-1", "タグA")); err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}

	pending, err := store.ListPending(ctx, otherUserID)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("他人の提案が見えている: %+v", pending)
	}

	count, err := store.CountPending(ctx, otherUserID)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if count != 0 {
		t.Errorf("他人の提案が件数に入っている: %d件", count)
	}
}

// 同じ記録を2人が別々に見ている場合。
//
// あるアカウントが別のアカウントのリポジトリをまとめて抱えている構成は
// 普通にあるので、**同じ記録IDが両方に現れる**。
// 主キーが利用者を含んでいないと、片方の判定がもう片方を消してしまう。
func TestSameRecordIsDecidedIndependentlyPerUser(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	suggestion := newSuggestion("shared-target", "タグA")
	for _, userID := range []string{testUserID, otherUserID} {
		if _, err := store.PutSuggestion(ctx, userID, suggestion); err != nil {
			t.Fatalf("想定外のエラー: %v", err)
		}
	}

	// 片方だけ却下する。
	if err := store.Decide(ctx, testUserID, suggestion.ID, DecisionRejected, time.Now()); err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}

	mine, err := store.ListPending(ctx, testUserID)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(mine) != 0 {
		t.Errorf("却下したのに残っている: %+v", mine)
	}

	theirs, err := store.ListPending(ctx, otherUserID)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(theirs) != 1 {
		t.Errorf("他人の判定に巻き込まれて消えた: %d件, want 1件", len(theirs))
	}
}

// 掃除も自分のぶんだけ。
func TestClearSuggestionsDoesNotTouchOtherUsers(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	for _, userID := range []string{testUserID, otherUserID} {
		if _, err := store.PutSuggestion(ctx, userID, newSuggestion("target-1", "タグA")); err != nil {
			t.Fatalf("想定外のエラー: %v", err)
		}
	}

	if err := store.ClearSuggestions(ctx, testUserID); err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}

	theirs, err := store.ListPending(ctx, otherUserID)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(theirs) != 1 {
		t.Errorf("他人の提案まで消えた: %d件, want 1件", len(theirs))
	}
}

// 利用者IDが空のまま保存先を触らせない。
//
// 空を通すと、その行はどの利用者からも見えない迷子になるか、
// 条件次第で他人に見えてしまう。
func TestStoreRejectsEmptyUserID(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if _, err := store.PutSuggestion(ctx, "", newSuggestion("target-1", "タグA")); err == nil {
		t.Error("利用者IDが空でも保存できてしまった")
	}
	if _, err := store.ListPending(ctx, ""); err == nil {
		t.Error("利用者IDが空でも一覧を引けてしまった")
	}
	if _, err := store.CountPending(ctx, ""); err == nil {
		t.Error("利用者IDが空でも件数を引けてしまった")
	}
	if err := store.ClearSuggestions(ctx, ""); err == nil {
		t.Error("利用者IDが空でも掃除できてしまった")
	}
	if err := store.MarkNoTagNeeded(ctx, "", "target-1", time.Now()); err == nil {
		t.Error("利用者IDが空でもタグ不要にできてしまった")
	}
	if err := store.MarkEvaluated(ctx, "", "target-1", "llm", time.Now()); err == nil {
		t.Error("利用者IDが空でも判定済みにできてしまった")
	}
	if _, err := store.DecidedTargetIDs(ctx, ""); err == nil {
		t.Error("利用者IDが空でも判定済みを引けてしまった")
	}
	if _, err := store.EvaluatedTargetIDs(ctx, ""); err == nil {
		t.Error("利用者IDが空でも評価済みを引けてしまった")
	}
}

func TestDecideRejectsUnknownDecision(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	suggestion := newSuggestion("target-1", "タグA")
	if _, err := store.PutSuggestion(ctx, testUserID,suggestion); err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}

	if err := store.Decide(ctx, testUserID,suggestion.ID, Decision("maybe"), time.Now()); err == nil {
		t.Fatal("知らない判定が通ってしまった")
	}
}

func TestCountPending(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	for _, tagName := range []string{"タグA", "タグB", "タグC"} {
		if _, err := store.PutSuggestion(ctx, testUserID,newSuggestion("target-1", tagName)); err != nil {
			t.Fatalf("想定外のエラー: %v", err)
		}
	}
	if err := store.Decide(ctx, testUserID,ids.SuggestionID("target-1", "タグA"), DecisionApproved, time.Now()); err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}

	count, err := store.CountPending(ctx, testUserID)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if count != 2 {
		t.Errorf("未判定 = %d件, want 2件", count)
	}
}

func TestListPendingIsSortedNewestFirst(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	older := newSuggestion("target-old", "タグA")
	older.RelatedTime = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := newSuggestion("target-new", "タグA")
	newer.RelatedTime = time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC)

	for _, suggestion := range []Suggestion{older, newer} {
		if _, err := store.PutSuggestion(ctx, testUserID,suggestion); err != nil {
			t.Fatalf("想定外のエラー: %v", err)
		}
	}

	pending, err := store.ListPending(ctx, testUserID)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("未判定 = %d件, want 2件", len(pending))
	}
	if pending[0].TargetID != "target-new" {
		t.Errorf("新しい順になっていない: %q が先頭", pending[0].TargetID)
	}
}

func TestReopeningStoreKeepsData(t *testing.T) {
	// 人間の判定はプロセスを跨いで残らなければ意味がない。
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")

	first, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("保存先を開けない: %v", err)
	}
	suggestion := newSuggestion("target-1", "タグA")
	if _, err := first.PutSuggestion(ctx, testUserID,suggestion); err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if err := first.Decide(ctx, testUserID,suggestion.ID, DecisionRejected, time.Now()); err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("閉じられない: %v", err)
	}

	second, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("開き直せない: %v", err)
	}
	defer func() { _ = second.Close() }()

	stored, err := second.PutSuggestion(ctx, testUserID,suggestion)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if stored {
		t.Error("開き直したら却下が効かなくなった")
	}
}
