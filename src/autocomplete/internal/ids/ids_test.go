package ids

import "testing"

func TestSuggestionIDIsStable(t *testing.T) {
	// 実行のたびに変わると、再解析で提案が重複し却下も効かなくなる。
	first := SuggestionID("target-1", "タグA")
	second := SuggestionID("target-1", "タグA")
	if first != second {
		t.Errorf("同じ入力で異なるID: %q != %q", first, second)
	}
}

func TestSuggestionIDDependsOnBothInputs(t *testing.T) {
	base := SuggestionID("target-1", "タグA")
	if SuggestionID("target-2", "タグA") == base {
		t.Error("対象が違うのに同じID")
	}
	if SuggestionID("target-1", "タグB") == base {
		t.Error("タグが違うのに同じID")
	}
}

func TestSuggestionIDSeparatesFields(t *testing.T) {
	// 区切りが無いと ("ab","c") と ("a","bc") が衝突する。
	if SuggestionID("ab", "c") == SuggestionID("a", "bc") {
		t.Error("対象IDとタグ名の境界が区別されていない")
	}
}

func TestTagIDDiffersFromSuggestionID(t *testing.T) {
	// 用途ごとに名前空間を分けていないと同じ値になる。
	if TagID("target-1", "タグA") == SuggestionID("target-1", "タグA") {
		t.Error("提案IDとタグIDが同じ値になっている")
	}
}

func TestTagIDIsStable(t *testing.T) {
	// gkill 側の重複検出が効くのはIDが安定していることが前提。
	// これが崩れると、手で消したタグを再承認のたびに蘇らせてしまう。
	if TagID("target-1", "タグA") != TagID("target-1", "タグA") {
		t.Error("同じ入力で異なるタグID")
	}
}

func TestIDsAreValidUUIDFormat(t *testing.T) {
	// gkill 側は文字列として受けるが、UUID の体裁は保つ。
	got := SuggestionID("target-1", "タグA")
	if len(got) != 36 {
		t.Errorf("UUIDの長さではない: %q", got)
	}
}
