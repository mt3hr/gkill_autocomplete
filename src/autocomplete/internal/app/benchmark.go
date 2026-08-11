package app

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/gkillclient"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/suggest"
)

// BenchmarkOptions は精度計測の条件。
type BenchmarkOptions struct {
	// From と To は計測に使う期間。この期間の記録は「正解が分かっているもの」として扱う。
	From time.Time
	To   time.Time
}

// TagScore はタグ1つぶんの成績。
type TagScore struct {
	Tag string
	// TruePositive は提案して実際に付いていた数。
	TruePositive int
	// FalsePositive は提案したが付いていなかった数。
	FalsePositive int
	// FalseNegative は付いていたのに提案しなかった数。
	FalseNegative int
}

// Precision は提案したもののうち当たっていた割合。
func (s TagScore) Precision() float64 {
	total := s.TruePositive + s.FalsePositive
	if total == 0 {
		return 0
	}
	return float64(s.TruePositive) / float64(total)
}

// Recall は付いていたもののうち提案できていた割合。
func (s TagScore) Recall() float64 {
	total := s.TruePositive + s.FalseNegative
	if total == 0 {
		return 0
	}
	return float64(s.TruePositive) / float64(total)
}

// BenchmarkReport は精度計測の結果。
type BenchmarkReport struct {
	LearnedRecords   int
	EvaluatedRecords int

	// SkippedRecords は機械が付けたタグしか無いために評価から外した数。
	// 実運用では候補にならない記録なので、混ぜると数字が実態からずれる。
	SkippedRecords int

	// TaggedRecords はもともとタグが付いていた記録の数。
	TaggedRecords int
	// UntaggedRecords はもともとタグが付いていなかった記録の数。
	// これを正解に含めないと実力を過大に見積もることになる。
	UntaggedRecords int

	TruePositive  int
	FalsePositive int
	FalseNegative int

	// ExactMatchRecords は提案した集合が実際のタグの集合と完全に一致した記録の数。
	ExactMatchRecords int
	// CorrectlySilentRecords はタグの無い記録に対して何も提案しなかった数。
	// これも正解であり、むしろ日々の手間を減らす上では重要。
	CorrectlySilentRecords int
	// WronglyNoisyRecords はタグの無い記録に対して提案してしまった数。
	WronglyNoisyRecords int

	PerTag []TagScore

	Elapsed time.Duration
}

// Precision は提案全体のうち当たっていた割合。
func (r BenchmarkReport) Precision() float64 {
	total := r.TruePositive + r.FalsePositive
	if total == 0 {
		return 0
	}
	return float64(r.TruePositive) / float64(total)
}

// Recall は付いていたタグのうち提案できていた割合。
func (r BenchmarkReport) Recall() float64 {
	total := r.TruePositive + r.FalseNegative
	if total == 0 {
		return 0
	}
	return float64(r.TruePositive) / float64(total)
}

// Benchmark は正解の分かっている期間で提案の精度を測る。
//
// 保存先には何も書き込まない。gkill にも書き込まない。
//
// 学習には計測期間より前の記録だけを使う。同じ期間を学習に混ぜると、
// 逐語一致が自分自身を引き当てて、実力とかけ離れた数字が出てしまう。
func (a *App) Benchmark(ctx context.Context, options BenchmarkOptions) (BenchmarkReport, error) {
	startedAt := a.now()
	report := BenchmarkReport{}

	if !options.From.Before(options.To) {
		return report, fmt.Errorf("計測期間が正しくありません (from=%s to=%s)", options.From.Format(time.DateOnly), options.To.Format(time.DateOnly))
	}

	records, err := a.fetchRecordsForBenchmark(ctx, options)
	if err != nil {
		return report, err
	}

	// 学習は計測期間より前だけ。ここを混ぜると数字が嘘になる。
	learnRecords := []suggest.Record{}
	evaluateRecords := []suggest.Record{}
	for _, record := range records {
		switch {
		case record.RelatedTime.Before(options.From):
			learnRecords = append(learnRecords, record)
		case record.RelatedTime.Before(options.To):
			evaluateRecords = append(evaluateRecords, record)
		}
	}
	report.LearnedRecords = len(learnRecords)
	report.EvaluatedRecords = len(evaluateRecords)

	knowledge := suggest.Learn(learnRecords, suggest.LearnOptions{
		ExcludeTagPatterns: a.Config.Exclude.TagPatterns,
		MaxExamples:        a.Config.Candidates.MaxFewShotExamples,
	})
	engine := suggest.NewEngine(knowledge, a.Config, a.Classifier)

	sorted := slices.Clone(records)
	sort.Slice(sorted, func(left int, right int) bool {
		return sorted[left].RelatedTime.Before(sorted[right].RelatedTime)
	})
	window := a.Config.Scoring.NeighborWindow()

	scores := map[string]*TagScore{}
	scoreOf := func(tagName string) *TagScore {
		score, ok := scores[tagName]
		if !ok {
			score = &TagScore{Tag: tagName}
			scores[tagName] = score
		}
		return score
	}

	for _, record := range evaluateRecords {
		if a.isOutOfPopulation(record) {
			report.SkippedRecords++
			continue
		}

		// 実際に付いているタグが正解。除外対象のタグは数えない。
		expected := map[string]bool{}
		for _, tagName := range record.Tags {
			if suggest.MatchesAnyPattern(tagName, a.Config.Exclude.TagPatterns) {
				continue
			}
			expected[tagName] = true
		}

		// タグを外した状態で判定させる。
		asUntagged := record
		asUntagged.Tags = nil

		result, err := engine.Suggest(ctx, asUntagged, neighborsOf(sorted, record, window))
		if err != nil {
			return report, fmt.Errorf("error at suggest for benchmark: %w", err)
		}

		suggested := map[string]bool{}
		for _, suggestion := range result.Suggestions {
			suggested[suggestion.Tag] = true
		}

		if len(expected) == 0 {
			report.UntaggedRecords++
			if len(suggested) == 0 {
				report.CorrectlySilentRecords++
				report.ExactMatchRecords++
			} else {
				report.WronglyNoisyRecords++
			}
		} else {
			report.TaggedRecords++
			if sameTagSet(expected, suggested) {
				report.ExactMatchRecords++
			}
		}

		for tagName := range suggested {
			if expected[tagName] {
				report.TruePositive++
				scoreOf(tagName).TruePositive++
			} else {
				report.FalsePositive++
				scoreOf(tagName).FalsePositive++
			}
		}
		for tagName := range expected {
			if !suggested[tagName] {
				report.FalseNegative++
				scoreOf(tagName).FalseNegative++
			}
		}
	}

	report.PerTag = make([]TagScore, 0, len(scores))
	for _, score := range scores {
		report.PerTag = append(report.PerTag, *score)
	}
	sort.Slice(report.PerTag, func(left int, right int) bool {
		leftTotal := report.PerTag[left].TruePositive + report.PerTag[left].FalseNegative
		rightTotal := report.PerTag[right].TruePositive + report.PerTag[right].FalseNegative
		if leftTotal != rightTotal {
			return leftTotal > rightTotal
		}
		return report.PerTag[left].Tag < report.PerTag[right].Tag
	})

	report.Elapsed = a.now().Sub(startedAt)
	return report, nil
}

// isOutOfPopulation は「実運用では提案の対象にならない記録」かを返す。
//
// 計測は「タグを外したつもりで判定させ、実際のタグと突き合わせる」形を取る。
// ところが機械が付けたタグしか無い記録は、実運用では**タグが付いている**ため
// 候補にならない。これを混ぜると、起こらない母集団を測ることになる。
//
// 費用の面でも大きい。自動収集の記録は文面が毎回違って逐語一致せず、
// 1件ずつ LLM に回ってしまう。日に数百件あるので、混ぜると計測が何時間もかかる。
//
// タグが1つも無い記録は除かない。実運用でも候補になり、
// 「何も提案しないのが正解」という重要な検証対象であるため。
func (a *App) isOutOfPopulation(record suggest.Record) bool {
	if !a.Config.Exclude.AlreadyTagged || len(record.Tags) == 0 {
		return false
	}
	for _, tagName := range record.Tags {
		if !suggest.MatchesAnyPattern(tagName, a.Config.Exclude.TagPatterns) {
			// 人が付けたタグが1つでもあれば、実運用でも候補だった記録。
			return false
		}
	}
	return true
}

// fetchRecordsForBenchmark は学習範囲と計測期間をまとめて取る。
func (a *App) fetchRecordsForBenchmark(ctx context.Context, options BenchmarkOptions) ([]suggest.Record, error) {
	query := &gkillclient.FindQuery{OnlyLatestData: true}

	learnStart := options.From.AddDate(0, 0, -a.Config.Scope.LearnDays)
	query.CalendarStartDate = &learnStart
	query.CalendarEndDate = &options.To

	repNames, err := a.resolveRepNames(ctx)
	if err != nil {
		return nil, err
	}
	if len(repNames) > 0 {
		query.Reps = repNames
	}

	kyous, err := a.Client.FetchKyous(ctx, query, gkillclient.FetchOptions{
		IncludeID: true,
		MaxTotal:  a.Config.Scope.MaxScanRecords,
	})
	if err != nil {
		return nil, err
	}
	a.warnIfScanTruncated(len(kyous))

	records := make([]suggest.Record, 0, len(kyous))
	allowedDataTypes := a.Config.Scope.DataTypes
	for _, kyou := range kyous {
		if len(allowedDataTypes) > 0 && !slices.Contains(allowedDataTypes, kyou.DataType) {
			continue
		}
		records = append(records, suggest.FromKyou(kyou))
	}
	return records, nil
}

func sameTagSet(left map[string]bool, right map[string]bool) bool {
	if len(left) != len(right) {
		return false
	}
	for tagName := range left {
		if !right[tagName] {
			return false
		}
	}
	return true
}

// Summary は計測結果を人が読める形で返す。
//
// これは利用者自身の端末に出すものなので、タグ名を含んでよい。
func (r BenchmarkReport) Summary() string {
	builder := &strings.Builder{}

	fmt.Fprintf(builder, "学習に使った記録: %d件（計測期間より前のみ）\n", r.LearnedRecords)
	fmt.Fprintf(builder, "計測した記録: %d件\n", r.EvaluatedRecords-r.SkippedRecords)
	fmt.Fprintf(builder, "  もともとタグあり: %d件\n", r.TaggedRecords)
	fmt.Fprintf(builder, "  もともとタグなし: %d件\n", r.UntaggedRecords)
	if r.SkippedRecords > 0 {
		fmt.Fprintf(builder, "  評価から外した記録: %d件 (機械が付けたタグしか無く、実運用では候補にならない)\n", r.SkippedRecords)
	}

	fmt.Fprintf(builder, "\nタグ単位:\n")
	fmt.Fprintf(builder, "  当たり(提案して実際に付いていた): %d\n", r.TruePositive)
	fmt.Fprintf(builder, "  外れ(提案したが付いていなかった): %d\n", r.FalsePositive)
	fmt.Fprintf(builder, "  取りこぼし(付いていたが提案せず): %d\n", r.FalseNegative)
	fmt.Fprintf(builder, "  適合率: %.1f%%  再現率: %.1f%%\n", r.Precision()*100, r.Recall()*100)

	fmt.Fprintf(builder, "\n記録単位:\n")
	fmt.Fprintf(builder, "  完全一致: %d / %d 件\n", r.ExactMatchRecords, r.EvaluatedRecords)
	fmt.Fprintf(builder, "  タグ不要を正しく黙った: %d / %d 件\n", r.CorrectlySilentRecords, r.UntaggedRecords)
	fmt.Fprintf(builder, "  タグ不要なのに提案した: %d 件\n", r.WronglyNoisyRecords)

	if len(r.PerTag) > 0 {
		fmt.Fprintf(builder, "\nタグごと(実際に付いていた数の多い順、上位10件):\n")
		for i, score := range r.PerTag {
			if i >= 10 {
				break
			}
			fmt.Fprintf(builder, "  %-16s 当たり%3d 外れ%3d 取りこぼし%3d  適合率%5.1f%% 再現率%5.1f%%\n",
				score.Tag, score.TruePositive, score.FalsePositive, score.FalseNegative,
				score.Precision()*100, score.Recall()*100)
		}
	}

	fmt.Fprintf(builder, "\n所要時間: %s\n", r.Elapsed.Round(time.Millisecond))
	return builder.String()
}
