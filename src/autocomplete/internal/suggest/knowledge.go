package suggest

import (
	"slices"
	"strings"
)

// ImageExample は写真の参考例。
//
// LLM に「このタグの記録はこう見える」と示すために使う。
// 学習済みモデルも埋め込みの索引も持たず、履歴から数枚選んで見せるだけ。
type ImageExample struct {
	RepName  string
	FileName string
}

// TagStat は1つのタグについて履歴から分かったこと。
type TagStat struct {
	Tag string

	// Count はそのタグが付いている記録の総数。
	Count int

	// ByContext は文脈ごとの出現数。候補タグの絞り込みに使う。
	ByContext map[string]int

	// HourHistogram は時刻ごとの出現数。
	HourHistogram [24]int

	// TextExamples は本文の参考例。
	TextExamples []string

	// ImageExamples は写真の参考例。
	ImageExamples []ImageExample
}

// Knowledge は履歴から学んだことのまとめ。
type Knowledge struct {
	// tags はタグ名ごとの統計。
	tags map[string]*TagStat

	// textToTags は正規化した本文から、その本文に付いていたタグ名への索引。
	// 同じ文面を繰り返す記録に同じタグを付ける習慣を、そのまま引き当てる。
	textToTags map[string]map[string]struct{}

	// contextTotals は文脈ごとの記録数。
	contextTotals map[string]int

	// contextUntagged は文脈ごとの「タグが付いていない記録」の数。
	// これが「タグを付けない」ことの起こりやすさを表す。
	contextUntagged map[string]int

	// maxTextExamples と maxImageExamples は参考例を貯める上限。
	maxTextExamples  int
	maxImageExamples int
}

// LearnOptions は学習の条件。
type LearnOptions struct {
	// ExcludeTagPatterns に当たるタグは学習しない。
	// 他のツールが機械的に付けたタグを候補から締め出すために使う。
	ExcludeTagPatterns []string

	// MaxExamples は候補タグ1つあたりに貯める参考例の数。
	MaxExamples int
}

// Learn は記録の履歴から学ぶ。
//
// 学習といっても統計を数えるだけで、モデルの訓練はしない。
// 「この文脈ではこのタグがよく使われる」「この文面にはこのタグが付く」
// 「この文脈ではそもそもタグを付けないことが多い」の3つが取れれば足りる。
func Learn(records []Record, options LearnOptions) *Knowledge {
	maxExamples := options.MaxExamples
	if maxExamples <= 0 {
		maxExamples = 4
	}

	knowledge := &Knowledge{
		tags:             map[string]*TagStat{},
		textToTags:       map[string]map[string]struct{}{},
		contextTotals:    map[string]int{},
		contextUntagged:  map[string]int{},
		maxTextExamples:  maxExamples,
		maxImageExamples: maxExamples,
	}

	for _, record := range records {
		contextKey := record.ContextKey()
		knowledge.contextTotals[contextKey]++

		usableTags := make([]string, 0, len(record.Tags))
		for _, tagName := range record.Tags {
			if MatchesAnyPattern(tagName, options.ExcludeTagPatterns) {
				continue
			}
			usableTags = append(usableTags, tagName)
		}

		if len(usableTags) == 0 {
			// タグを付けなかった記録も学習材料。
			// これを数えないと「付けない」という選択の重みが分からない。
			knowledge.contextUntagged[contextKey]++
			continue
		}

		normalizedText := NormalizeText(record.Text)
		if normalizedText != "" {
			byText, ok := knowledge.textToTags[normalizedText]
			if !ok {
				byText = map[string]struct{}{}
				knowledge.textToTags[normalizedText] = byText
			}
			for _, tagName := range usableTags {
				byText[tagName] = struct{}{}
			}
		}

		for _, tagName := range usableTags {
			knowledge.observe(tagName, contextKey, record, normalizedText)
		}
	}

	return knowledge
}

func (k *Knowledge) observe(tagName string, contextKey string, record Record, normalizedText string) {
	stat, ok := k.tags[tagName]
	if !ok {
		stat = &TagStat{Tag: tagName, ByContext: map[string]int{}}
		k.tags[tagName] = stat
	}

	stat.Count++
	stat.ByContext[contextKey]++
	stat.HourHistogram[record.RelatedTime.Hour()]++

	if normalizedText != "" && len(stat.TextExamples) < k.maxTextExamples {
		if !slices.Contains(stat.TextExamples, record.Text) {
			stat.TextExamples = append(stat.TextExamples, record.Text)
		}
	}

	if record.IsImage && record.FileName != "" && len(stat.ImageExamples) < k.maxImageExamples {
		stat.ImageExamples = append(stat.ImageExamples, ImageExample{
			RepName:  record.RepName,
			FileName: record.FileName,
		})
	}
}

// Candidate は判定にかける候補タグ1つ。
type Candidate struct {
	Tag string

	// Support はその文脈での実績件数。
	Support int

	// Prior は文脈と時刻から見た事前の確からしさ。0〜1。
	Prior float64

	// TextExamples と ImageExamples は LLM に見せる参考例。
	TextExamples  []string
	ImageExamples []ImageExample
}

// CandidatesOptions は候補タグの絞り込み条件。
type CandidatesOptions struct {
	MinExamples     int
	MaxCandidates   int
	TimeOfDayWeight float64
}

// Candidates はその記録に対して問う価値のある候補タグを返す。
//
// 全タグを総当たりしない。同じ文脈で実績のあるタグだけに絞ることで、
// LLM に渡す選択肢が現実的な数に収まり、判定の質も上がる。
func (k *Knowledge) Candidates(record Record, options CandidatesOptions) []Candidate {
	minExamples := max(options.MinExamples, 1)
	contextKey := record.ContextKey()

	candidates := make([]Candidate, 0, len(k.tags))
	for tagName, stat := range k.tags {
		support := stat.ByContext[contextKey]
		if support < minExamples {
			continue
		}
		candidates = append(candidates, Candidate{
			Tag:           tagName,
			Support:       support,
			Prior:         k.prior(stat, contextKey, record, options.TimeOfDayWeight),
			TextExamples:  slices.Clone(stat.TextExamples),
			ImageExamples: slices.Clone(stat.ImageExamples),
		})
	}

	// 実績の多い順。同数ならタグ名で安定させる。
	slices.SortFunc(candidates, func(left Candidate, right Candidate) int {
		if left.Support != right.Support {
			return right.Support - left.Support
		}
		return strings.Compare(left.Tag, right.Tag)
	})

	if options.MaxCandidates > 0 && len(candidates) > options.MaxCandidates {
		candidates = candidates[:options.MaxCandidates]
	}
	return candidates
}

// prior は文脈内での出現割合に、時刻の偏りを弱く足したもの。
//
// 時刻の重みを小さくしてあるのは、実際の記録では同じタグが一日中
// ばらけて現れ、時刻だけでは決め手にならないため。
func (k *Knowledge) prior(stat *TagStat, contextKey string, record Record, timeOfDayWeight float64) float64 {
	total := k.contextTotals[contextKey]
	if total == 0 {
		return 0
	}

	share := float64(stat.ByContext[contextKey]) / float64(total)
	if timeOfDayWeight <= 0 {
		return clamp01(share)
	}

	return clamp01(share*(1-timeOfDayWeight) + k.hourScore(stat, record)*timeOfDayWeight)
}

// hourScore はその時刻帯へのタグの偏りを0〜1で返す。
func (k *Knowledge) hourScore(stat *TagStat, record Record) float64 {
	if stat.Count == 0 {
		return 0
	}
	hour := record.RelatedTime.Hour()
	// 前後1時間を含めて数える。記録の時刻は数分ずれるのが普通なので。
	window := stat.HourHistogram[(hour+23)%24] + stat.HourHistogram[hour] + stat.HourHistogram[(hour+1)%24]

	share := float64(window) / float64(stat.Count)
	// 一様分布なら 3/24 = 0.125。その4倍(=0.5)で振り切る目盛りにする。
	return clamp01(share / 0.5)
}

// TagsForExactText はその本文と逐語一致する過去の記録に付いていたタグを返す。
func (k *Knowledge) TagsForExactText(text string) []string {
	normalized := NormalizeText(text)
	if normalized == "" {
		return nil
	}
	byText, ok := k.textToTags[normalized]
	if !ok {
		return nil
	}
	tagNames := make([]string, 0, len(byText))
	for tagName := range byText {
		tagNames = append(tagNames, tagName)
	}
	slices.Sort(tagNames)
	return tagNames
}

// UntaggedRate はその文脈で「タグを付けない」ことがどれだけ普通かを返す。
//
// これが高い文脈では、迷ったら提案しないほうが利用者の手間が減る。
func (k *Knowledge) UntaggedRate(contextKey string) float64 {
	total := k.contextTotals[contextKey]
	if total == 0 {
		return 0
	}
	return float64(k.contextUntagged[contextKey]) / float64(total)
}

// TagStatOf はタグの統計を返す。
func (k *Knowledge) TagStatOf(tagName string) (*TagStat, bool) {
	stat, ok := k.tags[tagName]
	return stat, ok
}

// TagNames は学習したタグ名を返す。
func (k *Knowledge) TagNames() []string {
	tagNames := make([]string, 0, len(k.tags))
	for tagName := range k.tags {
		tagNames = append(tagNames, tagName)
	}
	slices.Sort(tagNames)
	return tagNames
}

// ContextKeys は学習した文脈を記録数の多い順に返す。
func (k *Knowledge) ContextKeys() []string {
	contextKeys := make([]string, 0, len(k.contextTotals))
	for contextKey := range k.contextTotals {
		contextKeys = append(contextKeys, contextKey)
	}
	slices.SortFunc(contextKeys, func(left string, right string) int {
		if k.contextTotals[left] != k.contextTotals[right] {
			return k.contextTotals[right] - k.contextTotals[left]
		}
		return strings.Compare(left, right)
	})
	return contextKeys
}

// ContextTotal はその文脈の記録数を返す。
func (k *Knowledge) ContextTotal(contextKey string) int {
	return k.contextTotals[contextKey]
}

// MatchesAnyPattern はタグ名がいずれかのパターンに当たるかを返す。
//
// パターンは末尾の "*" だけを解する。前方一致で十分で、
// 一般の glob を持ち込むと設定の意味が読み取りづらくなる。
func MatchesAnyPattern(tagName string, patterns []string) bool {
	for _, pattern := range patterns {
		if matchesPattern(tagName, pattern) {
			return true
		}
	}
	return false
}

func matchesPattern(tagName string, pattern string) bool {
	if pattern == "" {
		return false
	}
	if prefix, found := strings.CutSuffix(pattern, "*"); found {
		return strings.HasPrefix(tagName, prefix)
	}
	return tagName == pattern
}

func clamp01(value float64) float64 {
	return min(max(value, 0), 1)
}
