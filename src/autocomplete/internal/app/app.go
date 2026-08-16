// Package app は各部品を繋いで、解析と設定生成という2つの仕事を行う。
//
// ログには件数と所要時間しか出さない。記録の本文・ファイル名・タグの中身は
// 出さない(利用者の生活の記録そのものであるため)。
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/classify"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/config"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/gkillclient"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/ids"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/llm"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/store"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/suggest"
)

const (
	// maxJudgeAttempts は1件の判定をやり直す回数(初回を含む)。
	//
	// LLM が落ちている間、繋がらないエラーは**即座に**返る。やり直さずに次の記録へ
	// 進むと、候補リストの最後まで1分あたり数千件の速さで「失敗」を積み上げてしまう。
	// 2026-08-16 に llama-server を入れ替えた1分のあいだ、残り12,425件すべてが
	// 失敗として流れ、それでも「解析が終わりました」と出て完走扱いになった。
	maxJudgeAttempts = 3

	// maxConsecutiveJudgeFailures は解析を打ち切るまでの連続失敗数。
	//
	// 1件ごとの失敗は飛ばして進んでよい(判定は記録ごとに独立している)が、
	// 続けて失敗するのは記録ではなく環境の問題なので、そのまま走り続けても
	// 候補を消費するだけで何も判定できない。**飛ばした記録は評価済みにしないので、
	// 打ち切っても失われるものは無い**(次の解析でやり直される)。
	maxConsecutiveJudgeFailures = 5
)

// judgeRetryWait はやり直しの間隔。
//
// モデルの読み込み直しは数十秒かかるので、間を置かないと3回とも同じ瞬間に当たる。
// テストが待たされないよう var にしてある(テストからは0にする)。
var judgeRetryWait = 10 * time.Second

// isRetryableJudgeError はやり直す価値のある失敗かを返す。
//
// 繋がらない・時間切れ・LLMがエラーを返した、の3つは環境側の一時的な事情
// (プロセスの入れ替え、モデルの読み込み中、一時的な過負荷)で起きうる。
// 応答を解釈できない・画像でないファイル、は同じ入力なら何度やっても同じなので
// やり直さない(無駄に3倍の時間を使うだけになる)。
func isRetryableJudgeError(err error) bool {
	return errors.Is(err, llm.ErrUnreachable) ||
		errors.Is(err, llm.ErrTimeout) ||
		errors.Is(err, llm.ErrRejected)
}

// judgeWithRetry は1件の判定を、一時的な失敗のときだけやり直す。
func (a *App) judgeWithRetry(ctx context.Context, engine *suggest.Engine, candidate suggest.Record, neighbors []suggest.Record) (suggest.Result, error) {
	var lastErr error
	for attempt := 1; attempt <= maxJudgeAttempts; attempt++ {
		result, err := engine.Suggest(ctx, candidate, neighbors)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !isRetryableJudgeError(err) {
			return suggest.Result{}, err
		}
		if attempt == maxJudgeAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return suggest.Result{}, ctx.Err()
		case <-time.After(judgeRetryWait):
		}
	}
	return suggest.Result{}, lastErr
}

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
		"取得が上限に達したため、記録が欠けています。"+
			"学習も候補選びも不完全になり、提案の質が落ちます。"+
			"scope.rep_prefixes で範囲を絞るか、scope.learn_days か scope.candidate_days を"+
			"縮めるか、scope.max_scan_records を上げてください",
		slog.Int("取得した記録", fetched),
		slog.Int("上限", limit),
		slog.Int("取得範囲の日数", a.fetchDays()),
		slog.Int("学習範囲の日数", a.Config.Scope.LearnDays),
		slog.Int("候補範囲の日数", a.Config.Scope.CandidateDays))
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
	// FetchedRecords は gkill から取ってきた記録の数。
	//
	// **LearnedRecords と別に持つ。** 学習の窓が候補の窓より狭いとき、
	// 取ってきた数と学習に使った数は一致しない。
	// 取得が上限で切られたことに気づくためにも、取得した数そのものが要る。
	FetchedRecords   int
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
	// FailedRecords は判定に失敗して飛ばした記録の数。
	//
	// **これが0でないことは異常ではあるが、解析の失敗ではない。**
	// LLM の応答が解釈できない・写真が取れない・1件が時間切れになる、
	// といったことは実際に起きる。1件のために残り全部を捨てるほうが害が大きい。
	FailedRecords int
	// FailureReason は最も多かった失敗の理由。
	//
	// **決め打ちの文字列しか入らない。** エラー本文には LLM の応答や
	// 記録の中身が混ざりうるので、そのままは載せない。
	// これが無いと、全件失敗しても件数しか分からず原因に辿り着けない。
	FailureReason string
	Elapsed       time.Duration
}

// Progress は解析の途中経過。
//
// 画面に「何件目か」を出すためだけのもの。記録の中身は入れない。
type Progress struct {
	// Done は判定を終えた記録の数(失敗して飛ばしたものも含む)。
	Done int
	// Total は判定する記録の総数。
	Total int
}

// Analyze はまだタグの付いていない記録に対する提案を作る。
func (a *App) Analyze(ctx context.Context) (AnalyzeReport, error) {
	return a.AnalyzeWithProgress(ctx, nil)
}

// AnalyzeWithProgress は途中経過を知らせながら解析する。
//
// onProgress は記録を1件片付けるたびに呼ばれる。nil でよい。
// **呼び出しは解析と同じゴルーチンで行うので、中で待たせないこと。**
func (a *App) AnalyzeWithProgress(ctx context.Context, onProgress func(Progress)) (AnalyzeReport, error) {
	startedAt := a.now()
	report := AnalyzeReport{}

	records, err := a.fetchRecords(ctx)
	if err != nil {
		return report, err
	}
	report.FetchedRecords = len(records)

	learnRecords := a.selectLearnRecords(records)
	report.LearnedRecords = len(learnRecords)

	knowledge := suggest.Learn(learnRecords, suggest.LearnOptions{
		ExcludeTagPatterns: a.Config.Exclude.TagPatterns,
		MaxExamples:        a.Config.Candidates.MaxFewShotExamples,
	})

	candidates, err := a.selectCandidates(ctx, records)
	if err != nil {
		return report, err
	}
	report.CandidateRecords = len(candidates)

	a.logger().Info("解析を開始します",
		slog.Int("取得した記録", report.FetchedRecords),
		slog.Int("学習した記録", report.LearnedRecords),
		slog.Int("判定する記録", report.CandidateRecords))

	// 総数が決まった時点で1回知らせる。画面はここで初めて分母を出せる。
	a.notifyProgress(onProgress, 0, len(candidates))

	engine := suggest.NewEngine(knowledge, a.Config, a.Classifier)

	// 近傍の記録を引くために時刻順に並べておく。
	sorted := slices.Clone(records)
	sort.Slice(sorted, func(left int, right int) bool {
		return sorted[left].RelatedTime.Before(sorted[right].RelatedTime)
	})
	window := a.Config.Scoring.NeighborWindow()

	// 失敗の理由ごとの件数。決め打ちの文字列だけを鍵にする。
	failureCounts := map[string]int{}

	// 続けて失敗した回数。1件でも判定できたら0に戻す。
	consecutiveFailures := 0

	for index, candidate := range candidates {
		// 中断は中断として扱う。利用者が止めたか、終了しようとしている。
		if err := ctx.Err(); err != nil {
			return report, err
		}

		neighbors := neighborsOf(sorted, candidate, window)

		result, err := a.judgeWithRetry(ctx, engine, candidate, neighbors)
		if err != nil {
			// **1件の失敗で残り全部を捨てない。**
			// 判定は記録ごとに独立しているので、失敗した1件を飛ばせば済む。
			// 落としてしまうと、後ろに並んでいる何十件もの記録が
			// 「解析が途中で止まった」という形で永久に処理されなくなる。
			//
			// 飛ばした記録は評価済みにしない。次に解析すればまた対象になる。
			if ctx.Err() != nil {
				return report, ctx.Err()
			}
			report.FailedRecords++

			// 記録の中身が混ざるので、エラー本文はログに出さない。
			// 代わりに決め打ちの理由へ落として数える。
			reason := failureReasonOf(err)
			failureCounts[reason]++

			// 同じ理由は最初の1回だけ出す。全件失敗したときに
			// 同じ行が千行並んでも、原因に近づかないため。
			if failureCounts[reason] == 1 {
				a.logger().Warn("記録の判定に失敗したため飛ばしました。次の解析でやり直します",
					slog.String("理由", reason),
					slog.Int("何件目", index+1),
					slog.Int("判定する記録", len(candidates)))
			}

			// **続けて失敗するなら打ち切る。**
			// 記録ではなく環境の問題なので、走り続けても候補を消費するだけで
			// 何も判定できない。飛ばした記録は評価済みにしていないので、
			// ここで止めても失われるものは無い。
			consecutiveFailures++
			if consecutiveFailures >= maxConsecutiveJudgeFailures {
				report.FailureReason = mostCommonReason(failureCounts)
				a.logger().Error("判定が続けて失敗したため解析を打ち切りました。原因を直してからやり直してください",
					slog.Int("続けて失敗した回数", consecutiveFailures),
					slog.String("理由", reason),
					slog.Int("判定できた記録", index+1-report.FailedRecords),
					slog.Int("判定する記録", len(candidates)))
				return report, fmt.Errorf("判定が%d件続けて失敗したため解析を打ち切りました: %s", consecutiveFailures, reason)
			}

			a.notifyProgress(onProgress, index+1, len(candidates))
			continue
		}
		consecutiveFailures = 0

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

		a.notifyProgress(onProgress, index+1, len(candidates))
	}

	report.FailureReason = mostCommonReason(failureCounts)
	report.Elapsed = a.now().Sub(startedAt)

	a.logger().Info("解析が終わりました",
		slog.Int("提案が出た記録", report.SuggestedRecords),
		slog.Int("提案が出なかった記録", report.NoSuggestionRecords),
		slog.Int("保存した提案", report.StoredSuggestions),
		slog.Int("判定に失敗した記録", report.FailedRecords),
		slog.Duration("所要時間", report.Elapsed))

	a.warnAboutFailures(report)
	return report, nil
}

// warnAboutFailures は判定に失敗した記録があったことを知らせる。
//
// **全件失敗と一部失敗は別のことなので、別の言い方をする。**
// 全件失敗は接続先か設定の問題で、記録を1件ずつ疑っても直らない。
func (a *App) warnAboutFailures(report AnalyzeReport) {
	if report.FailedRecords == 0 {
		return
	}

	if report.FailedRecords == report.CandidateRecords {
		a.logger().Error(
			"判定する記録がすべて失敗しました。個々の記録ではなく、接続先か設定の問題です。"+
				"提案は1件も作られていません",
			slog.String("理由", report.FailureReason),
			slog.Int("判定する記録", report.CandidateRecords))
		return
	}

	a.logger().Warn(
		"判定に失敗した記録があります。飛ばして先へ進めたので解析そのものは完走しています。"+
			"飛ばした記録は評価済みにしていないので、次の解析でやり直されます",
		slog.String("最も多かった理由", report.FailureReason),
		slog.Int("判定に失敗した記録", report.FailedRecords),
		slog.Int("判定する記録", report.CandidateRecords))
}

// failureReasonOf は判定の失敗を、記録の中身を含まない短い理由に落とす。
//
// **エラー本文をそのまま使ってはいけない。** LLM の応答にも写真の取得失敗にも
// 記録の中身が混ざりうるため、ここで決め打ちの文字列に置き換える。
// 理由には「次に何をすればよいか」まで入れる。件数だけでは動けない。
func failureReasonOf(err error) string {
	switch {
	case errors.Is(err, llm.ErrUnreachable):
		return "LLM に繋がらない (起動しているか、設定の llm.endpoint が合っているかを確かめてください)"
	case errors.Is(err, llm.ErrTimeout):
		return "LLM が時間切れ (llm.timeout_seconds を延ばすか、llm.thumb_size を小さくしてください)"
	case errors.Is(err, llm.ErrRejected):
		return "LLM がエラーを返した (モデル名と文脈長を確かめてください)"
	case errors.Is(err, llm.ErrBadResponse):
		return "LLM の応答を解釈できない"
	case errors.Is(err, gkillclient.ErrNotAnImage):
		return "画像でないファイルを画像として判定しようとした"
	case errors.Is(err, classify.ErrImageUnavailable):
		return "判定する写真を gkill から取得できない"
	default:
		return "その他"
	}
}

// mostCommonReason は最も多かった理由を返す。同数のときは名前で安定させる。
func mostCommonReason(counts map[string]int) string {
	best, bestCount := "", 0
	for reason, count := range counts {
		if count > bestCount || (count == bestCount && reason < best) {
			best, bestCount = reason, count
		}
	}
	return best
}

// notifyProgress は途中経過を知らせる。onProgress が nil なら何もしない。
func (a *App) notifyProgress(onProgress func(Progress), done int, total int) {
	if onProgress == nil {
		return
	}
	onProgress(Progress{Done: done, Total: total})
}

// fetchDays は gkill から取ってくる範囲を日数で返す。
//
// 学習の窓と候補の窓のうち**広いほう**。片方だけを見て取ると、
// もう片方が必要とする記録が手元に来ない。
//
// 候補のほうが広い場合(「昔の分まで候補に出したいが、判断は最近の習慣に
// 沿ってほしい」)は、取得した記録のうち学習の窓に入るものだけを
// selectLearnRecords が selects する。
func (a *App) fetchDays() int {
	return max(a.Config.Scope.LearnDays, a.Config.Scope.CandidateDays)
}

// selectLearnRecords は学習に使う記録を選ぶ。
//
// 候補の窓のほうが広いとき、取得した記録には学習の窓より古いものが混ざる。
// **それを学習に混ぜない。** 昔のタグの付け方に引きずられるのを避けるためで、
// これが「学習は直近1年・候補は全期間」を成り立たせている。
func (a *App) selectLearnRecords(records []suggest.Record) []suggest.Record {
	if a.Config.Scope.LearnDays >= a.fetchDays() {
		// 取得範囲がそのまま学習範囲。絞る必要がない。
		return records
	}

	learnStart := a.now().AddDate(0, 0, -a.Config.Scope.LearnDays)

	learnRecords := make([]suggest.Record, 0, len(records))
	for _, record := range records {
		if record.RelatedTime.Before(learnStart) {
			continue
		}
		learnRecords = append(learnRecords, record)
	}
	return learnRecords
}

// fetchRecords は学習と候補選びの両方に要る記録をまとめて取る。
//
// **取得は1回だけ。** 学習の窓と候補の窓のうち広いほうを取れば両方を賄える。
func (a *App) fetchRecords(ctx context.Context) ([]suggest.Record, error) {
	query := &gkillclient.FindQuery{OnlyLatestData: true}

	fetchStart := a.now().AddDate(0, 0, -a.fetchDays())
	query.CalendarStartDate = &fetchStart

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
