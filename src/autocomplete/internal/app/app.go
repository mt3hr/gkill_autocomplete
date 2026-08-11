// Package app は各部品を繋いで、解析と設定生成という2つの仕事を行う。
//
// ログには件数と所要時間しか出さない。記録の本文・ファイル名・タグの中身は
// 出さない(利用者の生活の記録そのものであるため)。
package app

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/config"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/gkillclient"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/ids"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/store"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/suggest"
)

// warnIfScanTruncated は取得が上限で打ち切られたことを知らせる。
//
// 打ち切られると学習が不完全なまま「正常終了」に見えてしまう。
// 黙って質を落とすのが一番まずいので、件数と直し方を出す。
func (a *App) warnIfScanTruncated(fetched int) {
	limit := a.Config.Scope.MaxScanRecords
	if limit <= 0 || fetched < limit {
		return
	}
	a.logger().Warn(
		"取得が上限に達したため、学習に使う記録が欠けています。"+
			"提案の質が落ちます。scope.rep_prefixes で範囲を絞るか、"+
			"scope.learn_days を縮めるか、scope.max_scan_records を上げてください",
		slog.Int("取得した記録", fetched),
		slog.Int("上限", limit),
		slog.Int("学習範囲の日数", a.Config.Scope.LearnDays))
}

// ModelLister は LLM で使えるモデルの名前を返せるもの。
//
// init が設定を組み立てるときにだけ使う。判定そのものには要らないので、
// nil でも構わない。
type ModelLister interface {
	ListModels(ctx context.Context) ([]string, error)
}

// App は解析を行う本体。
//
// **1つの App は1人ぶん。** Client が持つセッションはある利用者のものなので、
// 取得できる記録もその人のリポジトリに限られる。複数の利用者を扱うときは
// App を人数ぶん作る。保存先(Store)だけは共有し、行を UserID で分ける。
type App struct {
	Config     config.Config
	Client     *gkillclient.Client
	Store      *store.Store
	Classifier suggest.Classifier
	// Models は init がモデル名を調べるのに使う。判定には使わない。
	Models ModelLister
	Logger *slog.Logger

	// Now は現在時刻。テストから差し替える。
	Now func() time.Time
}

func (a *App) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *App) logger() *slog.Logger {
	if a.Logger != nil {
		return a.Logger
	}
	return slog.Default()
}

// UserID はこの App が扱う利用者を返す。保存先を絞る鍵になる。
func (a *App) UserID() string {
	return a.Client.UserID()
}

// AnalyzeReport は解析の結果。
//
// 中身は件数だけ。記録の内容は入れない。
type AnalyzeReport struct {
	LearnedRecords   int
	CandidateRecords int
	// SuggestedRecords は1件以上の提案が出た記録の数。
	SuggestedRecords int
	// NoSuggestionRecords は提案が0件だった記録の数。
	// これは失敗ではなく「タグを付けない」という正常な答え。
	NoSuggestionRecords int
	// StoredSuggestions は実際に保存された提案の数。
	StoredSuggestions int
	// SkippedByVerdict は過去の判定により保存しなかった提案の数。
	SkippedByVerdict int
	Elapsed          time.Duration
}

// Analyze はまだタグの付いていない記録に対する提案を作る。
func (a *App) Analyze(ctx context.Context) (AnalyzeReport, error) {
	startedAt := a.now()
	report := AnalyzeReport{}

	records, err := a.fetchRecords(ctx)
	if err != nil {
		return report, err
	}
	report.LearnedRecords = len(records)

	knowledge := suggest.Learn(records, suggest.LearnOptions{
		ExcludeTagPatterns: a.Config.Exclude.TagPatterns,
		MaxExamples:        a.Config.Candidates.MaxFewShotExamples,
	})

	candidates, err := a.selectCandidates(ctx, records)
	if err != nil {
		return report, err
	}
	report.CandidateRecords = len(candidates)

	a.logger().Info("解析を開始します",
		slog.Int("学習した記録", report.LearnedRecords),
		slog.Int("判定する記録", report.CandidateRecords))

	engine := suggest.NewEngine(knowledge, a.Config, a.Classifier)

	// 近傍の記録を引くために時刻順に並べておく。
	sorted := slices.Clone(records)
	sort.Slice(sorted, func(left int, right int) bool {
		return sorted[left].RelatedTime.Before(sorted[right].RelatedTime)
	})
	window := a.Config.Scoring.NeighborWindow()

	for _, candidate := range candidates {
		neighbors := neighborsOf(sorted, candidate, window)

		result, err := engine.Suggest(ctx, candidate, neighbors)
		if err != nil {
			return report, fmt.Errorf("error at suggest for record: %w", err)
		}

		if len(result.Suggestions) == 0 {
			report.NoSuggestionRecords++
		} else {
			report.SuggestedRecords++
		}

		for _, suggestion := range result.Suggestions {
			stored, err := a.Store.PutSuggestion(ctx, a.UserID(), store.Suggestion{
				ID:          ids.SuggestionID(candidate.ID, suggestion.Tag),
				TagID:       ids.TagID(candidate.ID, suggestion.Tag),
				TargetID:    candidate.ID,
				Tag:         suggestion.Tag,
				Confidence:  suggestion.Confidence,
				Tier:        suggestion.Tier,
				Reason:      suggestion.Reason,
				RepName:     candidate.RepName,
				DataType:    candidate.DataType,
				RelatedTime: candidate.RelatedTime,
				SuggestedAt: a.now(),
			})
			if err != nil {
				return report, err
			}
			if stored {
				report.StoredSuggestions++
			} else {
				report.SkippedByVerdict++
			}
		}

		if err := a.Store.MarkEvaluated(ctx, a.UserID(), candidate.ID, result.Tier, a.now()); err != nil {
			return report, err
		}
	}

	report.Elapsed = a.now().Sub(startedAt)

	a.logger().Info("解析が終わりました",
		slog.Int("提案が出た記録", report.SuggestedRecords),
		slog.Int("提案が出なかった記録", report.NoSuggestionRecords),
		slog.Int("保存した提案", report.StoredSuggestions),
		slog.Duration("所要時間", report.Elapsed))

	return report, nil
}

// fetchRecords は学習範囲の記録を取る。
//
// 学習範囲は候補範囲を必ず含む(設定の検証で保証している)ので、
// 取得は1回で済む。
func (a *App) fetchRecords(ctx context.Context) ([]suggest.Record, error) {
	query := &gkillclient.FindQuery{OnlyLatestData: true}

	learnStart := a.now().AddDate(0, 0, -a.Config.Scope.LearnDays)
	query.CalendarStartDate = &learnStart

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

// resolveRepNames は設定の接頭辞を実在のリポジトリ名へ展開する。
//
// 接頭辞が空のときは絞り込まない(全リポジトリが対象)。
func (a *App) resolveRepNames(ctx context.Context) ([]string, error) {
	prefixes := a.Config.Scope.RepPrefixes
	if len(prefixes) == 0 {
		return nil, nil
	}

	allRepNames, err := a.Client.GetAllRepNames(ctx)
	if err != nil {
		return nil, err
	}

	matched := make([]string, 0, len(allRepNames))
	for _, repName := range allRepNames {
		for _, prefix := range prefixes {
			if strings.HasPrefix(repName, prefix) {
				matched = append(matched, repName)
				break
			}
		}
	}
	if len(matched) == 0 {
		return nil, fmt.Errorf("設定の scope.rep_prefixes に一致するリポジトリがありません (%d 個の接頭辞を指定)", len(prefixes))
	}
	slices.Sort(matched)
	return matched, nil
}

// selectCandidates は提案の対象にする記録を選ぶ。
func (a *App) selectCandidates(ctx context.Context, records []suggest.Record) ([]suggest.Record, error) {
	decided, err := a.Store.DecidedTargetIDs(ctx, a.UserID())
	if err != nil {
		return nil, err
	}
	evaluated, err := a.Store.EvaluatedTargetIDs(ctx, a.UserID())
	if err != nil {
		return nil, err
	}

	candidateStart := a.now().AddDate(0, 0, -a.Config.Scope.CandidateDays)

	candidates := make([]suggest.Record, 0, len(records))
	for _, record := range records {
		if record.RelatedTime.Before(candidateStart) {
			continue
		}
		if a.Config.Exclude.AlreadyTagged && record.HasAnyTag() {
			continue
		}
		if _, ok := decided[record.ID]; ok {
			// 人が判定済み。蒸し返さない。
			continue
		}
		if _, ok := evaluated[record.ID]; ok {
			// 判定済み。同じ記録に LLM を二度呼ばない。
			continue
		}
		candidates = append(candidates, record)
	}
	return candidates, nil
}

// neighborsOf は対象の前後にある記録を返す。
//
// records は時刻の昇順に並んでいること。
func neighborsOf(records []suggest.Record, target suggest.Record, window time.Duration) []suggest.Record {
	if window <= 0 {
		return nil
	}

	from := sort.Search(len(records), func(i int) bool {
		return !records[i].RelatedTime.Before(target.RelatedTime.Add(-window))
	})

	neighbors := []suggest.Record{}
	for i := from; i < len(records); i++ {
		if records[i].RelatedTime.After(target.RelatedTime.Add(window)) {
			break
		}
		if records[i].ID == target.ID {
			continue
		}
		neighbors = append(neighbors, records[i])
	}
	return neighbors
}
