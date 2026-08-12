package suggest

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/config"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/gkillclient"
)

func at(hour int, minute int, second int) time.Time {
	return time.Date(2020, 6, 1, hour, minute, second, 0, time.UTC)
}

// imageRecord は写真の記録を作る。
func imageRecord(id string, repName string, when time.Time, tags ...string) Record {
	return Record{
		ID:          id,
		DataType:    "idf",
		RelatedTime: when,
		Tags:        tags,
		RepName:     repName,
		RepFamily:   RepFamilyOf(repName),
		IsImage:     true,
		FileName:    id + ".jpg",
	}
}

// textRecord は本文を持つ記録を作る。
func textRecord(id string, dataType string, text string, when time.Time, tags ...string) Record {
	return Record{
		ID:          id,
		DataType:    dataType,
		RelatedTime: when,
		Tags:        tags,
		Text:        text,
	}
}

func TestRepFamilyOf(t *testing.T) {
	cases := map[string]string{
		"SampleRep_DeviceA_20200101": "SampleRep",
		"SampleRep":                  "SampleRep",
		"":                           "",
		"With.Dot":                   "With.Dot",
		"A_B_C_D":                    "A",
	}
	for input, want := range cases {
		if got := RepFamilyOf(input); got != want {
			t.Errorf("RepFamilyOf(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNormalizeText(t *testing.T) {
	cases := []struct {
		name  string
		left  string
		right string
		same  bool
	}{
		{name: "前後の空白", left: "  ABC  ", right: "ABC", same: true},
		{name: "改行コードの違い", left: "A\r\nB", right: "A\nB", same: true},
		{name: "全角空白", left: "A　B", right: "A B", same: true},
		{name: "英字の大小", left: "Abc", right: "abc", same: true},
		{name: "空行", left: "A\n\n\nB", right: "A\nB", same: true},
		{name: "別の文面", left: "A", right: "B", same: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := NormalizeText(testCase.left) == NormalizeText(testCase.right)
			if got != testCase.same {
				t.Errorf("NormalizeText(%q) == NormalizeText(%q) は %v, want %v", testCase.left, testCase.right, got, testCase.same)
			}
		})
	}
}

func TestLearnCountsUntaggedRecords(t *testing.T) {
	// 意図的にタグを付けない記録がどれくらいあるかは、
	// 「提案しない」を正常な答えとして扱うための土台になる。
	records := []Record{
		imageRecord("1", "SampleRep_DeviceA_20200101", at(8, 0, 0), "タグA"),
		imageRecord("2", "SampleRep_DeviceA_20200101", at(9, 0, 0), "タグA"),
		imageRecord("3", "SampleRep_DeviceA_20200101", at(10, 0, 0)),
		imageRecord("4", "SampleRep_DeviceA_20200101", at(11, 0, 0)),
	}

	knowledge := Learn(records, LearnOptions{})

	rate := knowledge.UntaggedRate("rep:SampleRep")
	if rate != 0.5 {
		t.Errorf("タグ無しの割合 = %v, want 0.5", rate)
	}
}

func TestLearnGroupsDifferentDevicesTogether(t *testing.T) {
	// 端末を買い替えてもリポジトリ名の先頭は同じなので、
	// 同じ文脈として学習できなければならない。
	records := []Record{
		imageRecord("1", "SampleRep_DeviceA_20200101", at(8, 0, 0), "タグA"),
		imageRecord("2", "SampleRep_DeviceB_20220202", at(9, 0, 0), "タグA"),
	}

	knowledge := Learn(records, LearnOptions{})

	stat, ok := knowledge.TagStatOf("タグA")
	if !ok {
		t.Fatal("タグを学習していない")
	}
	if stat.ByContext["rep:SampleRep"] != 2 {
		t.Errorf("文脈ごとの件数 = %d, want 2", stat.ByContext["rep:SampleRep"])
	}
}

func TestLearnSkipsExcludedTags(t *testing.T) {
	// 他のツールが機械的に付けたタグを候補に混ぜない。
	// 混ざると件数で圧倒されて、手で付けているタグが埋もれる。
	records := []Record{
		textRecord("1", "kmemo", "本文", at(8, 0, 0), "example_auto_notification", "タグA"),
		textRecord("2", "kmemo", "本文2", at(9, 0, 0), "example_auto_media"),
	}

	knowledge := Learn(records, LearnOptions{ExcludeTagPatterns: []string{"example_auto_*"}})

	if _, ok := knowledge.TagStatOf("example_auto_notification"); ok {
		t.Error("除外したはずのタグを学習している")
	}
	if _, ok := knowledge.TagStatOf("タグA"); !ok {
		t.Error("除外対象でないタグを学習していない")
	}
	// 除外タグしか付いていない記録は「タグ無し」として数える。
	if rate := knowledge.UntaggedRate("type:kmemo"); rate != 0.5 {
		t.Errorf("タグ無しの割合 = %v, want 0.5", rate)
	}
}

func TestMatchesAnyPattern(t *testing.T) {
	patterns := []string{"example_auto_*", "完全一致"}
	cases := map[string]bool{
		"example_auto_media": true,
		"example_auto_":      true,
		"example_autox":      false,
		"完全一致":               true,
		"完全一致でない":            false,
		"":                   false,
	}
	for tagName, want := range cases {
		if got := MatchesAnyPattern(tagName, patterns); got != want {
			t.Errorf("MatchesAnyPattern(%q) = %v, want %v", tagName, got, want)
		}
	}
}

func TestCandidatesRespectMinExamples(t *testing.T) {
	records := []Record{}
	for i := range 6 {
		records = append(records, imageRecord("many", "SampleRep_DeviceA_20200101", at(8, i, 0), "よく使うタグ"))
	}
	records = append(records,
		imageRecord("rare1", "SampleRep_DeviceA_20200101", at(9, 0, 0), "たまに使うタグ"),
		imageRecord("rare2", "SampleRep_DeviceA_20200101", at(9, 1, 0), "たまに使うタグ"),
	)

	knowledge := Learn(records, LearnOptions{})
	target := imageRecord("new", "SampleRep_DeviceA_20200101", at(8, 30, 0))

	candidates := knowledge.Candidates(target, CandidatesOptions{MinExamples: 5})

	if len(candidates) != 1 {
		t.Fatalf("候補 = %d件, want 1件: %+v", len(candidates), candidates)
	}
	if candidates[0].Tag != "よく使うタグ" {
		t.Errorf("候補 = %q", candidates[0].Tag)
	}
}

func TestCandidatesAreLimitedAndOrdered(t *testing.T) {
	records := []Record{}
	// 出現数に差をつけて3種類のタグを作る。
	for i := range 5 {
		records = append(records, imageRecord("a", "SampleRep_DeviceA_20200101", at(8, i, 0), "多いタグ"))
	}
	for i := range 4 {
		records = append(records, imageRecord("b", "SampleRep_DeviceA_20200101", at(9, i, 0), "中くらいのタグ"))
	}
	for i := range 3 {
		records = append(records, imageRecord("c", "SampleRep_DeviceA_20200101", at(10, i, 0), "少ないタグ"))
	}

	knowledge := Learn(records, LearnOptions{})
	target := imageRecord("new", "SampleRep_DeviceA_20200101", at(8, 30, 0))

	candidates := knowledge.Candidates(target, CandidatesOptions{MinExamples: 1, MaxCandidates: 2})

	if len(candidates) != 2 {
		t.Fatalf("候補 = %d件, want 2件", len(candidates))
	}
	if candidates[0].Tag != "多いタグ" || candidates[1].Tag != "中くらいのタグ" {
		t.Errorf("実績の多い順になっていない: %q, %q", candidates[0].Tag, candidates[1].Tag)
	}
}

func TestExactTextMatchIsMultiLabel(t *testing.T) {
	// 同じ文面の記録に2つのタグが付いていたら、両方を提案する。
	// 最も確からしい1つを選ぶのではない。
	records := []Record{
		textRecord("1", "kmemo", "定型の本文", at(8, 0, 0), "タグA", "タグB"),
	}
	knowledge := Learn(records, LearnOptions{})
	engine := NewEngine(knowledge, config.Default(), nil)

	target := textRecord("new", "kmemo", "定型の本文", at(20, 0, 0))
	result, err := engine.Suggest(context.Background(), target, nil)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}

	if result.Tier != TierTextMatch {
		t.Errorf("tier = %q, want %q", result.Tier, TierTextMatch)
	}
	if len(result.Suggestions) != 2 {
		t.Fatalf("提案 = %d件, want 2件: %+v", len(result.Suggestions), result.Suggestions)
	}
}

func TestExactTextMatchIgnoresWhitespaceDifference(t *testing.T) {
	// 同じ操作を繰り返して生まれる記録は、空白の揺れを別にすれば同じ文面になる。
	records := []Record{
		textRecord("1", "kmemo", "定型の本文", at(8, 0, 0), "タグA"),
	}
	knowledge := Learn(records, LearnOptions{})
	engine := NewEngine(knowledge, config.Default(), nil)

	target := textRecord("new", "kmemo", "  定型の本文  ", at(20, 0, 0))
	result, err := engine.Suggest(context.Background(), target, nil)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(result.Suggestions) != 1 {
		t.Fatalf("提案 = %d件, want 1件", len(result.Suggestions))
	}
	if result.Suggestions[0].Tag != "タグA" {
		t.Errorf("提案 = %q", result.Suggestions[0].Tag)
	}
}

func TestZeroSuggestionsIsNormal(t *testing.T) {
	// 何も心当たりが無ければ0件を返す。無理に何かを提案しない。
	knowledge := Learn(nil, LearnOptions{})
	engine := NewEngine(knowledge, config.Default(), nil)

	result, err := engine.Suggest(context.Background(), textRecord("new", "kmemo", "見たことのない本文", at(12, 0, 0)), nil)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(result.Suggestions) != 0 {
		t.Errorf("提案 = %+v, want 0件", result.Suggestions)
	}
	if result.Tier != TierNone {
		t.Errorf("tier = %q, want %q", result.Tier, TierNone)
	}
}

func TestNeighborRecordProvidesHint(t *testing.T) {
	// 買い物の記録と写真のように、対になる記録が数秒差で並ぶことがある。
	// 片方の見出しがもう片方の手がかりになる。
	records := []Record{}
	for i := range 5 {
		records = append(records, imageRecord("past", "SampleRep_DeviceA_20200101", at(8, i, 0), "タグA"))
	}
	knowledge := Learn(records, LearnOptions{})
	engine := NewEngine(knowledge, config.Default(), nil)

	target := imageRecord("new", "SampleRep_DeviceA_20200101", at(13, 44, 56))
	neighbors := []Record{
		{ID: "expense", DataType: "nlog", Title: "タグA", RelatedTime: at(13, 45, 0)},
	}

	result, err := engine.Suggest(context.Background(), target, neighbors)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(result.Suggestions) != 1 {
		t.Fatalf("提案 = %d件, want 1件: %+v", len(result.Suggestions), result.Suggestions)
	}
	if result.Suggestions[0].Tier != TierContext {
		t.Errorf("tier = %q, want %q", result.Suggestions[0].Tier, TierContext)
	}
}

func TestNeighborOutsideWindowIsIgnored(t *testing.T) {
	records := []Record{}
	for i := range 5 {
		records = append(records, imageRecord("past", "SampleRep_DeviceA_20200101", at(8, i, 0), "タグA"))
	}
	knowledge := Learn(records, LearnOptions{})
	engine := NewEngine(knowledge, config.Default(), nil)

	target := imageRecord("new", "SampleRep_DeviceA_20200101", at(13, 0, 0))
	neighbors := []Record{
		// 既定の窓は5分。1時間離れていれば関係ない記録とみなす。
		{ID: "far", DataType: "nlog", Title: "タグA", RelatedTime: at(14, 0, 0)},
	}

	result, err := engine.Suggest(context.Background(), target, neighbors)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(result.Suggestions) != 0 {
		t.Errorf("離れた記録から提案してしまった: %+v", result.Suggestions)
	}
}

func TestThresholdFiltersWeakSuggestions(t *testing.T) {
	records := []Record{
		textRecord("1", "kmemo", "定型の本文", at(8, 0, 0), "タグA"),
	}
	knowledge := Learn(records, LearnOptions{})

	appConfig := config.Default()
	// 逐語一致の確信度より高い閾値にすれば、何も残らないはず。
	appConfig.Scoring.ExactTextMatchConfidence = 0.5
	appConfig.Scoring.Threshold = 0.9
	engine := NewEngine(knowledge, appConfig, nil)

	result, err := engine.Suggest(context.Background(), textRecord("new", "kmemo", "定型の本文", at(20, 0, 0)), nil)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(result.Suggestions) != 0 {
		t.Errorf("閾値未満の提案が残っている: %+v", result.Suggestions)
	}
}

func TestRuleNeverSuggestBlocksTag(t *testing.T) {
	records := []Record{
		textRecord("1", "kmemo", "定型の本文", at(8, 0, 0), "タグA", "タグB"),
	}
	knowledge := Learn(records, LearnOptions{})

	appConfig := config.Default()
	appConfig.Rules = []config.Rule{
		{When: config.RuleWhen{Tag: "タグB"}, NeverSuggest: true},
	}
	engine := NewEngine(knowledge, appConfig, nil)

	result, err := engine.Suggest(context.Background(), textRecord("new", "kmemo", "定型の本文", at(20, 0, 0)), nil)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(result.Suggestions) != 1 || result.Suggestions[0].Tag != "タグA" {
		t.Errorf("提案 = %+v, want タグA のみ", result.Suggestions)
	}
}

func TestRuleSuggestOverridesLearning(t *testing.T) {
	knowledge := Learn(nil, LearnOptions{})

	appConfig := config.Default()
	confidence := 0.99
	appConfig.Rules = []config.Rule{
		{
			When:       config.RuleWhen{TextEquals: "定型の本文"},
			Suggest:    []string{"タグA"},
			Confidence: &confidence,
		},
	}
	engine := NewEngine(knowledge, appConfig, nil)

	result, err := engine.Suggest(context.Background(), textRecord("new", "kmemo", "定型の本文", at(20, 0, 0)), nil)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if result.Tier != TierRule {
		t.Errorf("tier = %q, want %q", result.Tier, TierRule)
	}
	if len(result.Suggestions) != 1 || result.Suggestions[0].Confidence != 0.99 {
		t.Errorf("提案 = %+v", result.Suggestions)
	}
}

func TestRuleRepPrefixNarrowsScope(t *testing.T) {
	knowledge := Learn(nil, LearnOptions{})

	appConfig := config.Default()
	appConfig.Rules = []config.Rule{
		{When: config.RuleWhen{RepPrefix: "SampleRep"}, Suggest: []string{"タグA"}},
	}
	engine := NewEngine(knowledge, appConfig, nil)

	matched := imageRecord("a", "SampleRep_DeviceA_20200101", at(8, 0, 0))
	unmatched := imageRecord("b", "OtherRep_DeviceA_20200101", at(8, 0, 0))

	matchedResult, err := engine.Suggest(context.Background(), matched, nil)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(matchedResult.Suggestions) != 1 {
		t.Errorf("条件に合う記録に提案が出ていない: %+v", matchedResult.Suggestions)
	}

	unmatchedResult, err := engine.Suggest(context.Background(), unmatched, nil)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(unmatchedResult.Suggestions) != 0 {
		t.Errorf("条件に合わない記録に提案が出た: %+v", unmatchedResult.Suggestions)
	}
}

// stubClassifier は LLM の代わり。
type stubClassifier struct {
	judgements []Judgement
	err        error
	calls      int
	received   []Candidate
}

func (s *stubClassifier) Classify(_ context.Context, _ Record, candidates []Candidate) ([]Judgement, error) {
	s.calls++
	s.received = candidates
	if s.err != nil {
		return nil, s.err
	}
	return s.judgements, nil
}

func TestClassifierIsNotCalledWhenEarlierTierDecides(t *testing.T) {
	// 逐語一致で決まるなら LLM を呼ぶ必要はない。
	// 呼んでしまうと、遅いうえに判定がぶれる。
	records := []Record{
		textRecord("1", "kmemo", "定型の本文", at(8, 0, 0), "タグA"),
	}
	knowledge := Learn(records, LearnOptions{})
	classifier := &stubClassifier{}
	engine := NewEngine(knowledge, config.Default(), classifier)

	if _, err := engine.Suggest(context.Background(), textRecord("new", "kmemo", "定型の本文", at(20, 0, 0)), nil); err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if classifier.calls != 0 {
		t.Errorf("LLM を %d 回呼んでいる。逐語一致で決まっているのに", classifier.calls)
	}
}

func TestClassifierDecidesWhenEarlierTiersCannot(t *testing.T) {
	records := []Record{}
	for i := range 5 {
		records = append(records, imageRecord("past", "SampleRep_DeviceA_20200101", at(8, i, 0), "タグA"))
	}
	knowledge := Learn(records, LearnOptions{})

	classifier := &stubClassifier{judgements: []Judgement{
		{Tag: "タグA", Yes: true, Confidence: 0.9, Reason: "写真の中身から"},
	}}
	engine := NewEngine(knowledge, config.Default(), classifier)

	result, err := engine.Suggest(context.Background(), imageRecord("new", "SampleRep_DeviceA_20200101", at(13, 0, 0)), nil)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if classifier.calls != 1 {
		t.Errorf("LLM の呼び出し = %d回, want 1回", classifier.calls)
	}
	if result.Tier != TierLLM {
		t.Errorf("tier = %q, want %q", result.Tier, TierLLM)
	}
	if len(result.Suggestions) != 1 || result.Suggestions[0].Tag != "タグA" {
		t.Errorf("提案 = %+v", result.Suggestions)
	}
}

func TestClassifierIsSkippedWhenDampingMakesItHopeless(t *testing.T) {
	// ほとんどタグを付けていない場所では、LLM が満点を返しても
	// dampenByHabit の割り引きで閾値を割るため、提案は必ず0個になる。
	// **その呼び出しを最初からしない。** 1件5秒前後かかるので、
	// 割合が大きいと判定が桁違いに遅くなる(実測で16万件が10日コースになった)。
	//
	// タグ付き5件・タグ無し20件 -> 未タグ率 0.8 -> keepRate 0.2 < 閾値 0.6。
	records := []Record{}
	for i := range 5 {
		records = append(records, imageRecord("tagged", "SampleRep_DeviceA_20200101", at(8, i, 0), "タグA"))
	}
	for i := range 20 {
		records = append(records, imageRecord("untagged", "SampleRep_DeviceA_20200101", at(9, i, 0)))
	}
	knowledge := Learn(records, LearnOptions{})

	classifier := &stubClassifier{judgements: []Judgement{
		{Tag: "タグA", Yes: true, Confidence: 1.0, Reason: "満点でも通らない"},
	}}
	engine := NewEngine(knowledge, config.Default(), classifier)

	result, err := engine.Suggest(context.Background(), imageRecord("new", "SampleRep_DeviceA_20200101", at(13, 0, 0)), nil)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if classifier.calls != 0 {
		t.Errorf("LLM を %d 回呼んでいる。満点でも足切りされる文脈なので呼ぶ意味がない", classifier.calls)
	}
	if result.Tier != TierHopeless {
		t.Errorf("tier = %q, want %q", result.Tier, TierHopeless)
	}
	if len(result.Suggestions) != 0 {
		t.Errorf("提案 = %+v, want 0件", result.Suggestions)
	}
}

func TestClassifierNoIsRespected(t *testing.T) {
	// 候補に挙げても LLM が否定したら提案しない。
	records := []Record{}
	for i := range 5 {
		records = append(records, imageRecord("past", "SampleRep_DeviceA_20200101", at(8, i, 0), "タグA"))
	}
	knowledge := Learn(records, LearnOptions{})

	classifier := &stubClassifier{judgements: []Judgement{
		{Tag: "タグA", Yes: false, Confidence: 0.95},
	}}
	engine := NewEngine(knowledge, config.Default(), classifier)

	result, err := engine.Suggest(context.Background(), imageRecord("new", "SampleRep_DeviceA_20200101", at(13, 0, 0)), nil)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(result.Suggestions) != 0 {
		t.Errorf("否定された候補が提案された: %+v", result.Suggestions)
	}
}

func TestLLMSuggestionIsDampenedWhereTaggingIsRare(t *testing.T) {
	// 実測で踏んだもの。写真の保管庫に対して LLM が9件すべて確信度 1.0 を返し、
	// 「〜の可能性があります」という後付けの理由を付けていた。
	// 自己申告をそのまま使うと閾値が何も濾さない。
	//
	// ほとんどタグを付けない場所では黙るのが正しい。
	records := []Record{}
	// 5件はタグあり、95件はタグ無し → タグを付ける割合は 5%。
	for i := range 5 {
		records = append(records, imageRecord("tagged", "SampleRep_DeviceA_20200101", at(8, i, 0), "タグA"))
	}
	for i := range 95 {
		records = append(records, imageRecord("bare", "SampleRep_DeviceA_20200101", at(9, i%60, 0)))
	}
	knowledge := Learn(records, LearnOptions{})

	classifier := &stubClassifier{judgements: []Judgement{
		{Tag: "タグA", Yes: true, Confidence: 1.0, Reason: "似ています"},
	}}
	engine := NewEngine(knowledge, config.Default(), classifier)

	result, err := engine.Suggest(context.Background(), imageRecord("new", "SampleRep_DeviceA_20200101", at(13, 0, 0)), nil)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	// 1.0 × 0.05 = 0.05 で、既定の閾値 0.6 に届かない。
	if len(result.Suggestions) != 0 {
		t.Errorf("めったにタグを付けない場所で提案が出た: %+v", result.Suggestions)
	}
}

func TestLLMSuggestionSurvivesWhereTaggingIsCommon(t *testing.T) {
	// よくタグを付けている場所では、割り引いても残ること。
	// 割り引きが強すぎると、本来出したい提案まで消える。
	records := []Record{}
	// 74件はタグあり、26件はタグ無し → 実データの飲み物の写真に近い比率。
	for i := range 74 {
		records = append(records, imageRecord("tagged", "SampleRep_DeviceA_20200101", at(8, i%60, 0), "タグA"))
	}
	for i := range 26 {
		records = append(records, imageRecord("bare", "SampleRep_DeviceA_20200101", at(9, i, 0)))
	}
	knowledge := Learn(records, LearnOptions{})

	classifier := &stubClassifier{judgements: []Judgement{
		{Tag: "タグA", Yes: true, Confidence: 1.0},
	}}
	engine := NewEngine(knowledge, config.Default(), classifier)

	result, err := engine.Suggest(context.Background(), imageRecord("new", "SampleRep_DeviceA_20200101", at(13, 0, 0)), nil)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(result.Suggestions) != 1 {
		t.Fatalf("提案 = %d件, want 1件", len(result.Suggestions))
	}
	// 1.0 × 0.74 = 0.74。
	if got := result.Suggestions[0].Confidence; got < 0.73 || got > 0.75 {
		t.Errorf("確信度 = %v, want 約0.74", got)
	}
}

func TestExactTextMatchIsNotDampened(t *testing.T) {
	// 逐語一致は推測ではなく直接の証拠なので割り引かない。
	// めったにタグを付けない場所でも、同じ文面に同じタグが付いていた事実は動かない。
	records := []Record{
		textRecord("1", "kmemo", "定型の本文", at(8, 0, 0), "タグA"),
	}
	for i := range 99 {
		records = append(records, textRecord("bare", "kmemo", "ばらばらの本文"+string(rune('a'+i%26)), at(9, i%60, 0)))
	}
	knowledge := Learn(records, LearnOptions{})
	engine := NewEngine(knowledge, config.Default(), nil)

	result, err := engine.Suggest(context.Background(), textRecord("new", "kmemo", "定型の本文", at(20, 0, 0)), nil)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(result.Suggestions) != 1 {
		t.Fatalf("提案 = %d件, want 1件 (逐語一致が割り引かれている)", len(result.Suggestions))
	}
	if got := result.Suggestions[0].Confidence; got != config.Default().Scoring.ExactTextMatchConfidence {
		t.Errorf("確信度 = %v, want %v", got, config.Default().Scoring.ExactTextMatchConfidence)
	}
}

func TestClassifierErrorIsReported(t *testing.T) {
	records := []Record{}
	for i := range 5 {
		records = append(records, imageRecord("past", "SampleRep_DeviceA_20200101", at(8, i, 0), "タグA"))
	}
	knowledge := Learn(records, LearnOptions{})

	classifier := &stubClassifier{err: errors.New("模擬的な失敗")}
	engine := NewEngine(knowledge, config.Default(), classifier)

	if _, err := engine.Suggest(context.Background(), imageRecord("new", "SampleRep_DeviceA_20200101", at(13, 0, 0)), nil); err == nil {
		t.Fatal("LLM の失敗が握り潰されている")
	}
}

func TestClassifierReceivesOnlyRelevantCandidates(t *testing.T) {
	// 全タグを総当たりで問うと、選択肢が多すぎて判定が荒れる。
	records := []Record{}
	for i := range 5 {
		records = append(records, imageRecord("a", "SampleRep_DeviceA_20200101", at(8, i, 0), "同じ文脈のタグ"))
	}
	for i := range 5 {
		records = append(records, textRecord("b", "kmemo", "別文脈", at(9, i, 0), "別文脈のタグ"))
	}
	knowledge := Learn(records, LearnOptions{})

	classifier := &stubClassifier{}
	engine := NewEngine(knowledge, config.Default(), classifier)

	if _, err := engine.Suggest(context.Background(), imageRecord("new", "SampleRep_DeviceA_20200101", at(13, 0, 0)), nil); err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(classifier.received) != 1 || classifier.received[0].Tag != "同じ文脈のタグ" {
		t.Errorf("LLM に渡した候補 = %+v", classifier.received)
	}
}

func TestSuggestionsAreSortedByConfidence(t *testing.T) {
	records := []Record{}
	for i := range 5 {
		records = append(records, imageRecord("past", "SampleRep_DeviceA_20200101", at(8, i, 0), "タグA", "タグB"))
	}
	knowledge := Learn(records, LearnOptions{})

	classifier := &stubClassifier{judgements: []Judgement{
		{Tag: "タグA", Yes: true, Confidence: 0.7},
		{Tag: "タグB", Yes: true, Confidence: 0.95},
	}}
	engine := NewEngine(knowledge, config.Default(), classifier)

	result, err := engine.Suggest(context.Background(), imageRecord("new", "SampleRep_DeviceA_20200101", at(13, 0, 0)), nil)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(result.Suggestions) != 2 {
		t.Fatalf("提案 = %d件, want 2件", len(result.Suggestions))
	}
	if result.Suggestions[0].Tag != "タグB" {
		t.Errorf("確信度の高い順になっていない: %+v", result.Suggestions)
	}
}

func TestFromKyouBuildsSearchableText(t *testing.T) {
	kyou := gkillclient.Kyou{
		ID:          "x",
		DataType:    "nlog",
		RelatedTime: at(13, 45, 0),
		Payload:     json.RawMessage(`{"kind":"nlog","title":"品名","shop":"店名","amount":-160}`),
	}

	record := FromKyou(kyou)

	if record.Title != "品名" {
		t.Errorf("title = %q", record.Title)
	}
	// 見出しも店名も判定の手がかりになるので、両方を照合対象に含める。
	for _, want := range []string{"品名", "店名"} {
		if !strings.Contains(record.Text, want) {
			t.Errorf("本文に %q が含まれていない: %q", want, record.Text)
		}
	}
}

func TestFromKyouExtractsRepFamilyForImages(t *testing.T) {
	kyou := gkillclient.Kyou{
		ID:          "x",
		DataType:    "idf",
		RelatedTime: at(8, 0, 0),
		Payload:     json.RawMessage(`{"kind":"idf","file_name":"a.jpg","is_image":true,"rep_name":"SampleRep_DeviceA_20200101"}`),
	}

	record := FromKyou(kyou)

	if !record.IsImage {
		t.Error("写真として扱われていない")
	}
	if record.RepFamily != "SampleRep" {
		t.Errorf("rep family = %q, want SampleRep", record.RepFamily)
	}
	if record.ContextKey() != "rep:SampleRep" {
		t.Errorf("context = %q", record.ContextKey())
	}
}

func TestContextKeyFallsBackToDataType(t *testing.T) {
	// 写真以外はリポジトリ名が取れないので、記録の種別を文脈にする。
	record := textRecord("x", "kmemo", "本文", at(8, 0, 0))
	if record.ContextKey() != "type:kmemo" {
		t.Errorf("context = %q, want type:kmemo", record.ContextKey())
	}
}
