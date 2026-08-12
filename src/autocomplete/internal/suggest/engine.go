package suggest

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/config"
)

// 判定がどの段階で決まったかを表す。利用者に根拠を示すために持ち回る。
const (
	// TierRule は設定に書かれたルールで決まった。
	TierRule = "rule"
	// TierTextMatch は本文が過去の記録と逐語一致した。
	TierTextMatch = "text_match"
	// TierContext は近くの記録や語の一致から決まった。
	TierContext = "context"
	// TierLLM は LLM が判定した。
	TierLLM = "llm"
	// TierNone はどの段階でも決まらなかった(提案0個)。
	TierNone = "none"
	// TierHopeless は LLM を呼んでも結果が必ず足切りされるため呼ばなかった。
	//
	// **提案0個という意味では TierNone と同じ。** 別の名前にしてあるのは、
	// 「候補が無かった」のか「呼んでも無駄だった」のかを後から数えられるようにするため。
	// 判定の速さはこの割合でほぼ決まるので、見えないと調整のしようがない。
	TierHopeless = "hopeless"
)

// 各段階の基準となる確信度。
//
// 逐語一致は設定から取る(利用者が調整できる)。それ以外はここで決める。
const (
	// ownTextConfidence は記録自身の本文にタグ名が現れた場合。
	ownTextConfidence = 0.8
	// neighborConfidence は近い時刻の別の記録にタグ名が現れた場合。
	// 自分の本文より弱い手がかりなので少し下げる。
	neighborConfidence = 0.7
)

// Suggestion は1つの記録に対する提案1件。
type Suggestion struct {
	Tag        string
	Confidence float64
	Tier       string
	// Reason は人向けの短い説明。記録の本文をそのまま入れないこと
	// (画面にもログにも出るため)。
	Reason string
}

// Judgement は候補タグ1つに対する LLM の判定。
type Judgement struct {
	Tag        string
	Yes        bool
	Confidence float64
	Reason     string
}

// Classifier は候補タグごとに yes/no を返すもの。
//
// LLM を使う実装を差し込むための境目。nil のままでも
// 逐語一致と文脈による判定は動く。
type Classifier interface {
	Classify(ctx context.Context, record Record, candidates []Candidate) ([]Judgement, error)
}

// Engine は提案を作る。
type Engine struct {
	knowledge  *Knowledge
	scoring    config.ScoringConfig
	candidates config.CandidatesConfig
	rules      []config.Rule
	classifier Classifier
}

// NewEngine は提案器を作る。classifier は nil でもよい。
func NewEngine(knowledge *Knowledge, appConfig config.Config, classifier Classifier) *Engine {
	return &Engine{
		knowledge:  knowledge,
		scoring:    appConfig.Scoring,
		candidates: appConfig.Candidates,
		rules:      appConfig.Rules,
		classifier: classifier,
	}
}

// Result は1つの記録に対する判定の結果。
type Result struct {
	// Suggestions は閾値を超えた提案。0件は正常な結果。
	Suggestions []Suggestion
	// Tier はどの段階まで進んだか。同じ記録に LLM を二度呼ばないための記録に使う。
	Tier string
}

// Suggest は記録1件に対する提案を作る。
//
// neighbors には対象の前後にある記録を渡す。買い物の記録と写真のように、
// 対になる記録が数秒差で並ぶことがあり、片方の見出しがもう片方の手がかりになる。
//
// 上の段階で決まったら下は見ない。本文が過去と逐語一致するなら、
// それ以上推測する必要はない。
func (e *Engine) Suggest(ctx context.Context, record Record, neighbors []Record) (Result, error) {
	denied, denyAll := e.deniedTags(record, neighbors)
	if denyAll {
		return Result{Suggestions: nil, Tier: TierRule}, nil
	}

	if fromRules := e.suggestByRules(record, neighbors, denied); len(fromRules) > 0 {
		return Result{Suggestions: e.finish(fromRules), Tier: TierRule}, nil
	}

	if fromText := e.suggestByExactText(record, denied); len(fromText) > 0 {
		return Result{Suggestions: e.finish(fromText), Tier: TierTextMatch}, nil
	}

	candidates := e.candidatesFor(record, denied)
	if len(candidates) == 0 {
		return Result{Suggestions: nil, Tier: TierNone}, nil
	}

	if fromContext := e.suggestByContext(record, neighbors, candidates); len(fromContext) > 0 {
		return Result{Suggestions: e.finish(e.dampenByHabit(record, fromContext)), Tier: TierContext}, nil
	}

	if e.classifier == nil {
		return Result{Suggestions: nil, Tier: TierNone}, nil
	}

	// **結果が必ず捨てられるなら LLM を呼ばない。**
	//
	// LLM の確信度は最大 1.0 で、そのあと dampenByHabit が
	// keepRate = 1 - UntaggedRate(文脈) を掛け、finish が閾値未満を落とす。
	// つまり割り引き後の上限は keepRate そのものなので、
	// keepRate が閾値に届かない文脈では、満点の答えが返っても必ず落ちる。
	//
	// **提案の中身は1件も変わらない。** 捨てられると分かっている答えを
	// 作る手間だけが消える。
	//
	// これが効くのは、ほとんどタグを付けていない場所の記録が大半を占めるため。
	// ある実環境では判定の61.7%が LLM に流れ、そのうち約9割が提案0個で
	// 終わっていた。1件あたり5.3秒かかるので、16万件の判定が10日規模になっていた。
	// この関門を入れたあとは LLM に流れるのが1.0%になり、判定は
	// 11件/分から1,284件/分になった。
	if !e.canSurviveHabitDamping(record) {
		return Result{Suggestions: nil, Tier: TierHopeless}, nil
	}

	judgements, err := e.classifier.Classify(ctx, record, candidates)
	if err != nil {
		return Result{}, fmt.Errorf("error at classify record: %w", err)
	}

	fromLLM := make([]Suggestion, 0, len(judgements))
	for _, judgement := range judgements {
		if !judgement.Yes || denied[judgement.Tag] {
			continue
		}
		fromLLM = append(fromLLM, Suggestion{
			Tag:        judgement.Tag,
			Confidence: clamp01(judgement.Confidence),
			Tier:       TierLLM,
			Reason:     judgement.Reason,
		})
	}
	return Result{Suggestions: e.finish(e.dampenByHabit(record, fromLLM)), Tier: TierLLM}, nil
}

// canSurviveHabitDamping は、割り引きを受けても閾値を超えられる余地があるかを返す。
//
// dampenByHabit と finish の対を先読みしているだけで、判定そのものはしない。
// **ここを変えるときは dampenByHabit と finish の両方を見ること。**
// 割り引き方や足切りの仕方が変わると、この先読みが嘘になる。
func (e *Engine) canSurviveHabitDamping(record Record) bool {
	keepRate := 1 - e.knowledge.UntaggedRate(record.ContextKey())
	if keepRate >= 1 {
		// 割り引きが無い文脈。dampenByHabit も素通りする。
		return true
	}
	return keepRate >= e.scoring.Threshold
}

// dampenByHabit は「その場でそもそもタグを付けるか」で確信度を割り引く。
//
//	割り引いた確信度 = もとの確信度 × (1 − その文脈でタグを付けない割合)
//
// これが要るのは、推測から出た確信度が当てにならないため。
// 実測では、写真の保管庫に対する LLM の判定9件が**すべて 1.0** を返し、
// 「〜という名前をもつ可能性があります」のような後付けの理由が付いていた。
// 自己申告をそのまま閾値にかけても、何も濾せない。
//
// 一方で「その場所でタグを付けているか」は履歴から数えられる事実である。
// ほとんどタグを付けない場所では、よほど強い証拠が無い限り黙るのが正しい。
//
// 逐語一致と設定のルールは割り引かない。推測ではなく直接の証拠だから。
func (e *Engine) dampenByHabit(record Record, suggestions []Suggestion) []Suggestion {
	keepRate := 1 - e.knowledge.UntaggedRate(record.ContextKey())
	if keepRate >= 1 {
		return suggestions
	}

	dampened := make([]Suggestion, 0, len(suggestions))
	for _, suggestion := range suggestions {
		suggestion.Confidence = clamp01(suggestion.Confidence * keepRate)
		dampened = append(dampened, suggestion)
	}
	return dampened
}

// deniedTags は「提案してはいけないタグ」を集める。
//
// 条件にタグ名が無い never_suggest は、その記録に対して何も提案しないという意味。
func (e *Engine) deniedTags(record Record, neighbors []Record) (denied map[string]bool, denyAll bool) {
	denied = map[string]bool{}
	for _, rule := range e.rules {
		if !rule.NeverSuggest {
			continue
		}
		if !recordMatchesRule(rule.When, record, neighbors, e.scoring.NeighborWindow()) {
			continue
		}
		if rule.When.Tag == "" {
			return denied, true
		}
		denied[rule.When.Tag] = true
	}
	return denied, false
}

// suggestByRules は設定に書かれた決め打ちのルールを適用する。
func (e *Engine) suggestByRules(record Record, neighbors []Record, denied map[string]bool) []Suggestion {
	suggestions := []Suggestion{}
	for _, rule := range e.rules {
		if rule.NeverSuggest || len(rule.Suggest) == 0 {
			continue
		}
		if !recordMatchesRule(rule.When, record, neighbors, e.scoring.NeighborWindow()) {
			continue
		}
		confidence := e.scoring.ExactTextMatchConfidence
		if rule.Confidence != nil {
			confidence = *rule.Confidence
		}
		for _, tagName := range rule.Suggest {
			if denied[tagName] {
				continue
			}
			suggestions = append(suggestions, Suggestion{
				Tag:        tagName,
				Confidence: clamp01(confidence),
				Tier:       TierRule,
				Reason:     "設定のルールに一致",
			})
		}
	}
	return suggestions
}

// suggestByExactText は本文が過去の記録と逐語一致する場合の提案。
//
// 同じ操作を繰り返したときの定型の記録は字面がそのまま一致するので、
// この段階だけで大半が片付く。LLM を呼ぶ必要がない。
func (e *Engine) suggestByExactText(record Record, denied map[string]bool) []Suggestion {
	if strings.TrimSpace(record.Text) == "" {
		return nil
	}

	tagNames := e.knowledge.TagsForExactText(record.Text)
	suggestions := make([]Suggestion, 0, len(tagNames))
	for _, tagName := range tagNames {
		if denied[tagName] {
			continue
		}
		suggestions = append(suggestions, Suggestion{
			Tag:        tagName,
			Confidence: clamp01(e.scoring.ExactTextMatchConfidence),
			Tier:       TierTextMatch,
			Reason:     "同じ本文の記録に付いていたタグ",
		})
	}
	return suggestions
}

// suggestByContext は近くの記録と語の一致から提案する。
func (e *Engine) suggestByContext(record Record, neighbors []Record, candidates []Candidate) []Suggestion {
	ownText := strings.ToLower(record.Text + "\n" + record.Title)
	window := e.scoring.NeighborWindow()

	suggestions := []Suggestion{}
	for _, candidate := range candidates {
		lowerTag := strings.ToLower(candidate.Tag)
		if lowerTag == "" {
			continue
		}

		if strings.Contains(ownText, lowerTag) {
			suggestions = append(suggestions, Suggestion{
				Tag:        candidate.Tag,
				Confidence: ownTextConfidence,
				Tier:       TierContext,
				Reason:     "記録の中にタグ名と同じ語がある",
			})
			continue
		}

		if neighborMentions(neighbors, record.RelatedTime, window, lowerTag) {
			suggestions = append(suggestions, Suggestion{
				Tag:        candidate.Tag,
				Confidence: neighborConfidence,
				Tier:       TierContext,
				Reason:     "近い時刻の別の記録にタグ名と同じ語がある",
			})
		}
	}
	return suggestions
}

// candidatesFor はその記録に問う候補タグを返す。
//
// 設定のルールが候補を固定している場合はそちらを使う。
func (e *Engine) candidatesFor(record Record, denied map[string]bool) []Candidate {
	if fixed := e.fixedCandidates(record); fixed != nil {
		return filterDeniedCandidates(fixed, denied)
	}

	found := e.knowledge.Candidates(record, CandidatesOptions{
		MinExamples:     e.candidates.MinExamples,
		MaxCandidates:   e.candidates.MaxCandidateTags,
		TimeOfDayWeight: e.scoring.TimeOfDayWeight,
	})
	return filterDeniedCandidates(found, denied)
}

// fixedCandidates は設定で候補タグが固定されていればそれを返す。
func (e *Engine) fixedCandidates(record Record) []Candidate {
	for _, rule := range e.rules {
		if len(rule.CandidateTags) == 0 {
			continue
		}
		if !recordMatchesRule(rule.When, record, nil, 0) {
			continue
		}
		fixed := make([]Candidate, 0, len(rule.CandidateTags))
		for _, tagName := range rule.CandidateTags {
			candidate := Candidate{Tag: tagName}
			if stat, ok := e.knowledge.TagStatOf(tagName); ok {
				candidate.Support = stat.ByContext[record.ContextKey()]
				candidate.TextExamples = slices.Clone(stat.TextExamples)
				candidate.ImageExamples = slices.Clone(stat.ImageExamples)
			}
			fixed = append(fixed, candidate)
		}
		return fixed
	}
	return nil
}

// finish は閾値で足切りし、確信度の高い順に整える。
//
// 同じタグが複数の経路から出た場合は最も高い確信度のものだけを残す。
func (e *Engine) finish(suggestions []Suggestion) []Suggestion {
	best := map[string]Suggestion{}
	for _, suggestion := range suggestions {
		if suggestion.Confidence < e.scoring.Threshold {
			continue
		}
		existing, ok := best[suggestion.Tag]
		if !ok || suggestion.Confidence > existing.Confidence {
			best[suggestion.Tag] = suggestion
		}
	}

	deduped := make([]Suggestion, 0, len(best))
	for _, suggestion := range best {
		deduped = append(deduped, suggestion)
	}
	slices.SortFunc(deduped, func(left Suggestion, right Suggestion) int {
		if left.Confidence != right.Confidence {
			if left.Confidence > right.Confidence {
				return -1
			}
			return 1
		}
		return strings.Compare(left.Tag, right.Tag)
	})
	return deduped
}

func filterDeniedCandidates(candidates []Candidate, denied map[string]bool) []Candidate {
	kept := make([]Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if denied[candidate.Tag] {
			continue
		}
		kept = append(kept, candidate)
	}
	return kept
}

// recordMatchesRule はルールの条件が記録に当てはまるかを返す。
// 指定された条件がすべて成り立ったときだけ真。
func recordMatchesRule(when config.RuleWhen, record Record, neighbors []Record, window time.Duration) bool {
	if when.RepPrefix != "" && !strings.HasPrefix(record.RepName, when.RepPrefix) {
		return false
	}
	if when.DataType != "" && record.DataType != when.DataType {
		return false
	}
	if when.TextEquals != "" && NormalizeText(record.Text) != NormalizeText(when.TextEquals) {
		return false
	}
	if when.TextContains != "" && !strings.Contains(strings.ToLower(record.Text), strings.ToLower(when.TextContains)) {
		return false
	}
	if when.NeighborTitleContains != "" {
		if !neighborMentions(neighbors, record.RelatedTime, window, strings.ToLower(when.NeighborTitleContains)) {
			return false
		}
	}
	return true
}

// neighborMentions は指定の時間内にある別の記録が語を含むかを返す。
func neighborMentions(neighbors []Record, at time.Time, window time.Duration, lowerWord string) bool {
	if window <= 0 || lowerWord == "" {
		return false
	}
	for _, neighbor := range neighbors {
		gap := neighbor.RelatedTime.Sub(at)
		if gap < 0 {
			gap = -gap
		}
		if gap > window {
			continue
		}
		if strings.Contains(strings.ToLower(neighbor.Title), lowerWord) {
			return true
		}
		if strings.Contains(strings.ToLower(neighbor.Text), lowerWord) {
			return true
		}
	}
	return false
}
