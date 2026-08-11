package app

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/config"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/llm"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/suggest"
)

// 設定を自動生成するときの判断基準。
//
// 「人が選んでタグを付けている場所」を見つけて、そこだけを対象にするのが狙い。
// 機械的に全件へ同じタグが付く場所は、提案する余地が無いので外す。
const (
	// minContextTotal はこの数に満たない文脈は判断材料が足りないとみなす。
	minContextTotal = 20

	// machineTagShare は1つのタグがこれ以上を占める文脈を
	// 「機械が付けている」とみなす境目。人が選んでいるなら偏りはここまで行かない。
	machineTagShare = 0.95

	// maxUntaggedRate はこれを超える文脈を「そもそもタグを付けない場所」とみなす。
	maxUntaggedRate = 0.95

	// maxSelectedContexts は自動選択する文脈の数の上限。
	maxSelectedContexts = 8
)

// ContextProfile は文脈1つぶんの使われ方。
type ContextProfile struct {
	ContextKey string
	// RepFamily はリポジトリ由来の文脈のときだけ埋まる。
	RepFamily string

	Total    int
	Untagged int
	// TagCounts はタグごとの出現数。
	TagCounts map[string]int

	// DominantTag は最も多いタグ。DominantShare はタグ付き記録に占める割合。
	DominantTag   string
	DominantShare float64
}

// UntaggedRate はタグを付けていない記録の割合。
func (p ContextProfile) UntaggedRate() float64 {
	if p.Total == 0 {
		return 0
	}
	return float64(p.Untagged) / float64(p.Total)
}

// DistinctTags は使われているタグの種類数。
func (p ContextProfile) DistinctTags() int {
	return len(p.TagCounts)
}

// LooksMachineTagged は機械が一律に付けている文脈かを返す。
//
// 1種類のタグがほぼ全件を占め、かつタグ無しがほとんど無いなら、
// そこには人の選択が入っていない。提案する余地も無い。
func (p ContextProfile) LooksMachineTagged() bool {
	return p.DominantShare >= machineTagShare && p.UntaggedRate() < (1-machineTagShare)
}

// LooksHandTagged は人が選んでタグを付けている文脈かを返す。
func (p ContextProfile) LooksHandTagged() bool {
	if p.Total < minContextTotal {
		return false
	}
	if p.LooksMachineTagged() {
		return false
	}
	if p.UntaggedRate() > maxUntaggedRate {
		return false
	}
	// 2種類以上のタグを使い分けているということは、そこで選択が起きている。
	return p.DistinctTags() >= 2
}

// ProfileContexts は記録を文脈ごとに集計する。
func ProfileContexts(records []suggest.Record) []ContextProfile {
	byContext := map[string]*ContextProfile{}

	for _, record := range records {
		contextKey := record.ContextKey()
		profile, ok := byContext[contextKey]
		if !ok {
			profile = &ContextProfile{
				ContextKey: contextKey,
				RepFamily:  record.RepFamily,
				TagCounts:  map[string]int{},
			}
			byContext[contextKey] = profile
		}

		profile.Total++
		if len(record.Tags) == 0 {
			profile.Untagged++
			continue
		}
		for _, tagName := range record.Tags {
			profile.TagCounts[tagName]++
		}
	}

	profiles := make([]ContextProfile, 0, len(byContext))
	for _, profile := range byContext {
		tagged := profile.Total - profile.Untagged
		for tagName, count := range profile.TagCounts {
			share := 0.0
			if tagged > 0 {
				share = float64(count) / float64(tagged)
			}
			if share > profile.DominantShare || (share == profile.DominantShare && tagName < profile.DominantTag) {
				profile.DominantShare = share
				profile.DominantTag = tagName
			}
		}
		profiles = append(profiles, *profile)
	}

	// 記録数の多い順。同数なら文脈名で安定させる。
	sort.Slice(profiles, func(left int, right int) bool {
		if profiles[left].Total != profiles[right].Total {
			return profiles[left].Total > profiles[right].Total
		}
		return profiles[left].ContextKey < profiles[right].ContextKey
	})
	return profiles
}

// InitReport は設定生成の結果。
type InitReport struct {
	ScannedRecords int
	Profiles       []ContextProfile
	// SelectedContexts は提案の対象に選んだ文脈。
	SelectedContexts []string
	// ExcludedTags は候補から外すことにしたタグ。
	ExcludedTags []string

	// AvailableModels は LLM で使えたモデルの名前。
	AvailableModels []string
	// ChosenTextModel と ChosenVisionModel は設定に書き込んだモデル。
	ChosenTextModel   string
	ChosenVisionModel string
	// ModelLookupError はモデルを調べられなかった理由。調べられた場合は空。
	ModelLookupError string
}

// BuildInitialConfig は稼働中の gkill を解析して設定を組み立てる。
//
// 出来上がる設定には利用者のリポジトリ名とタグ名が入る。この生成物は
// リポジトリの外に置かれるものであり、ソースコードには決して焼き込まない。
func (a *App) BuildInitialConfig(ctx context.Context) (config.Config, InitReport, error) {
	report := InitReport{}

	// 全体を見渡すため、範囲を絞らずに読む。上限で歯止めをかける。
	scanConfig := a.Config
	scanConfig.Scope.RepPrefixes = nil
	scanner := &App{Config: scanConfig, Client: a.Client, Store: a.Store, Logger: a.Logger, Now: a.Now}

	records, err := scanner.fetchRecords(ctx)
	if err != nil {
		return config.Config{}, report, err
	}
	report.ScannedRecords = len(records)
	report.Profiles = ProfileContexts(records)

	built := a.Config
	repPrefixes := []string{}
	excludedTags := []string{}

	for _, profile := range report.Profiles {
		if profile.LooksMachineTagged() && profile.DominantTag != "" {
			// 機械が一律に付けているタグ。候補に混ざると件数で圧倒してしまう。
			if !slices.Contains(excludedTags, profile.DominantTag) {
				excludedTags = append(excludedTags, profile.DominantTag)
			}
			continue
		}
		if !profile.LooksHandTagged() {
			continue
		}
		if len(report.SelectedContexts) >= maxSelectedContexts {
			continue
		}
		report.SelectedContexts = append(report.SelectedContexts, profile.ContextKey)
		if profile.RepFamily != "" && !slices.Contains(repPrefixes, profile.RepFamily) {
			repPrefixes = append(repPrefixes, profile.RepFamily)
		}
	}

	slices.Sort(repPrefixes)
	slices.Sort(excludedTags)
	built.Scope.RepPrefixes = repPrefixes
	built.Exclude.TagPatterns = excludedTags

	a.fillModels(ctx, &built, &report)

	if err := config.Validate(built); err != nil {
		return config.Config{}, report, fmt.Errorf("生成した設定が検証に通りません: %w", err)
	}

	report.ExcludedTags = excludedTags
	return built, report, nil
}

// fillModels は LLM に問い合わせて、使えるモデルを設定へ書き込む。
//
// LLM が動いていなくても init は失敗させない。モデルの指定が無くても
// 逐語一致と近くの記録による判定は動くので、後から書き足せばよい。
func (a *App) fillModels(ctx context.Context, built *config.Config, report *InitReport) {
	if a.Models == nil {
		return
	}

	available, err := a.Models.ListModels(ctx)
	if err != nil {
		report.ModelLookupError = err.Error()
		return
	}
	report.AvailableModels = available

	// 利用者が既に書いている場合は触らない。
	if built.LLM.TextModel == "" {
		built.LLM.TextModel = pickTextModel(available)
	}
	if built.LLM.VisionModel == "" {
		built.LLM.VisionModel = pickVisionModel(available)
	}

	report.ChosenTextModel = built.LLM.TextModel
	report.ChosenVisionModel = built.LLM.VisionModel
}

// pickVisionModel は写真を扱えそうなモデルを選ぶ。
func pickVisionModel(available []string) string {
	for _, name := range available {
		if llm.LooksLikeVisionModel(name) {
			return name
		}
	}
	return ""
}

// pickTextModel は本文を扱うモデルを選ぶ。
//
// 視覚モデルでも本文は扱えるが、写真の分だけ重いので、
// 写真向けでないものを優先する。
func pickTextModel(available []string) string {
	for _, name := range available {
		if !llm.LooksLikeVisionModel(name) {
			return name
		}
	}
	if len(available) > 0 {
		return available[0]
	}
	return ""
}

// Summary は生成結果の要約を人が読める形で返す。
//
// これは利用者自身の端末に出すものなので、リポジトリ名やタグ名を含んでよい。
// 一方でリポジトリの中のファイルには決して書かない。
func (r InitReport) Summary() string {
	builder := &strings.Builder{}
	fmt.Fprintf(builder, "読んだ記録: %d件\n", r.ScannedRecords)

	fmt.Fprintf(builder, "\n見つかった文脈(記録数の多い順、上位10件):\n")
	for i, profile := range r.Profiles {
		if i >= 10 {
			break
		}
		kind := "対象外"
		switch {
		case slices.Contains(r.SelectedContexts, profile.ContextKey):
			kind = "対象"
		case profile.LooksMachineTagged():
			kind = "機械付与"
		}
		fmt.Fprintf(builder, "  [%s] %s  記録%d件 / タグ%d種 / タグ無し%.0f%%\n",
			kind, profile.ContextKey, profile.Total, profile.DistinctTags(), profile.UntaggedRate()*100)
	}

	if len(r.SelectedContexts) == 0 {
		fmt.Fprintf(builder, "\n提案の対象になる文脈が見つかりませんでした。"+
			"設定の scope.rep_prefixes を手で指定してください。\n")
	}
	if len(r.ExcludedTags) > 0 {
		fmt.Fprintf(builder, "\n候補から外すタグ: %s\n", strings.Join(r.ExcludedTags, ", "))
	}

	builder.WriteString(r.modelSummary())
	return builder.String()
}

// modelSummary は LLM のモデルについての要約を返す。
func (r InitReport) modelSummary() string {
	builder := &strings.Builder{}

	if r.ModelLookupError != "" {
		fmt.Fprintf(builder, "\nLLM のモデルを調べられませんでした:\n  %s\n", r.ModelLookupError)
		fmt.Fprintf(builder, "  LLM を起動してから設定の llm.text_model / llm.vision_model を書いてください。\n")
		fmt.Fprintf(builder, "  書かなくても、本文の一致と近くの記録による判定は動きます。\n")
		return builder.String()
	}

	if len(r.AvailableModels) == 0 {
		return builder.String()
	}

	fmt.Fprintf(builder, "\nLLM で使えるモデル: %s\n", strings.Join(r.AvailableModels, ", "))

	if r.ChosenTextModel != "" {
		fmt.Fprintf(builder, "  本文の判定に選びました: %s\n", r.ChosenTextModel)
	} else {
		fmt.Fprintf(builder, "  本文の判定に使えるモデルが見つかりませんでした。\n")
	}

	if r.ChosenVisionModel != "" {
		fmt.Fprintf(builder, "  写真の判定に選びました: %s\n", r.ChosenVisionModel)
	} else {
		fmt.Fprintf(builder, "  写真を扱えるモデルが見つかりませんでした。写真の判定を使うには、\n")
		fmt.Fprintf(builder, "  視覚モデルを用意してから設定の llm.vision_model に書いてください。\n")
	}

	fmt.Fprintf(builder, "  名前から選んでいるので、違っていれば設定を直してください。\n")
	return builder.String()
}
