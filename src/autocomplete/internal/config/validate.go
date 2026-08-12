package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// thumbSizeRe は gkill 側が受け付けるサムネ指定の書式。
// gkill の idf_thumb_file_server.go と同じ正規表現。
var thumbSizeRe = regexp.MustCompile(`^(\d{1,4})x(\d{1,4})$`)

// maxThumbSide は gkill 側のサムネ上限。これを超えると gkill は
// エラーにせず原本(全画素)を返すので、こちらで弾く。
const maxThumbSide = 1024

// Validate は設定を検証する。
//
// 見つかった問題はすべて集めて返す。1つ直すたびに起動し直す羽目にならないよう、
// 最初の1件で打ち切らない。
func Validate(c Config) error {
	var problems []error

	problems = append(problems, validateServer(c.Server)...)
	problems = append(problems, validateGkill(c.Gkill)...)
	problems = append(problems, validateLLM(c.LLM)...)
	problems = append(problems, validateScope(c.Scope)...)
	problems = append(problems, validateCandidates(c.Candidates)...)
	problems = append(problems, validateScoring(c.Scoring)...)
	problems = append(problems, validateRules(c.Rules)...)

	return errors.Join(problems...)
}

func validateServer(c ServerConfig) []error {
	if strings.TrimSpace(c.Listen) == "" {
		return []error{errors.New("server.listen が空です")}
	}

	// ループバック以外へ開いてよい。画面は gkill のアカウントによる
	// ログインで守られ、通信は gkill と同じ証明書で暗号化される。
	// 書式だけ確かめる。
	if _, err := IsLoopbackListenAddr(c.Listen); err != nil {
		return []error{fmt.Errorf("server.listen を解釈できません (%q): %w", c.Listen, err)}
	}
	return nil
}

func validateGkill(c GkillConfig) []error {
	var problems []error

	// 空でよい。空なら server_config.db から組み立てる。
	if strings.TrimSpace(c.BaseURL) != "" {
		if _, err := url.Parse(c.BaseURL); err != nil {
			problems = append(problems, fmt.Errorf("gkill.base_url を解釈できません: %w", err))
		}
	}

	if c.TimeoutSeconds <= 0 {
		problems = append(problems, fmt.Errorf("gkill.timeout_seconds は1以上にしてください (現在 %d)", c.TimeoutSeconds))
	}

	return problems
}

func validateLLM(c LLMConfig) []error {
	var problems []error

	if strings.TrimSpace(c.Endpoint) == "" {
		problems = append(problems, errors.New("llm.endpoint が空です"))
	} else {
		loopback, err := IsLoopbackURL(c.Endpoint)
		switch {
		case err != nil:
			problems = append(problems, fmt.Errorf("llm.endpoint を解釈できません (%q): %w", c.Endpoint, err))
		case !loopback && !c.AllowRemote:
			// このアプリの最優先の制約。
			// LLM には記録の本文と写真そのものを渡すので、既定では外へ出さない。
			problems = append(problems, fmt.Errorf(
				"llm.endpoint がループバックではありません (%q)。"+
					"判定にはあなたの記録の本文と写真を渡すため、既定では外部への送信を拒否します。"+
					"意味を理解した上で許可する場合のみ llm.allow_remote を true にしてください", c.Endpoint))
		}
	}

	if c.TimeoutSeconds <= 0 {
		problems = append(problems, fmt.Errorf("llm.timeout_seconds は1以上にしてください (現在 %d)", c.TimeoutSeconds))
	}

	if _, _, ok := ParseThumbSize(c.ThumbSize); !ok {
		problems = append(problems, fmt.Errorf(
			"llm.thumb_size が不正です (%q)。'400x400' の形式で、各辺 1〜%d にしてください。"+
				"範囲外だと gkill はエラーを返さず原本(全画素)を返します", c.ThumbSize, maxThumbSide))
	}

	return problems
}

func validateScope(c ScopeConfig) []error {
	var problems []error

	if c.CandidateDays <= 0 {
		problems = append(problems, fmt.Errorf("scope.candidate_days は1以上にしてください (現在 %d)", c.CandidateDays))
	}
	if c.LearnDays <= 0 {
		problems = append(problems, fmt.Errorf("scope.learn_days は1以上にしてください (現在 %d)", c.LearnDays))
	}
	// **learn_days < candidate_days は許す。**
	//
	// かつては拒否していた。取得範囲が learn_days で決まっていた頃は、
	// candidate_days を広く書いても実際には learn_days ぶんしか候補にならず、
	// それが黙って起きるより起動を止めるほうがましだったため。
	//
	// いまは取得範囲が両者の広いほうになり、学習の窓は名前どおり
	// 「学習に使う範囲」を指す。「昔の分まで候補に出したいが、判断は
	// 最近の習慣に沿ってほしい」を素直に書けるようになったので、拒否しない。
	if c.MaxScanRecords <= 0 {
		problems = append(problems, fmt.Errorf("scope.max_scan_records は1以上にしてください (現在 %d)", c.MaxScanRecords))
	}

	return problems
}

func validateCandidates(c CandidatesConfig) []error {
	var problems []error

	if c.MinExamples < 1 {
		problems = append(problems, fmt.Errorf("candidates.min_examples は1以上にしてください (現在 %d)", c.MinExamples))
	}
	if c.MaxCandidateTags < 1 {
		problems = append(problems, fmt.Errorf("candidates.max_candidate_tags は1以上にしてください (現在 %d)", c.MaxCandidateTags))
	}
	if c.MaxFewShotExamples < 0 {
		problems = append(problems, fmt.Errorf("candidates.max_few_shot_examples は0以上にしてください (現在 %d)", c.MaxFewShotExamples))
	}

	return problems
}

func validateScoring(c ScoringConfig) []error {
	var problems []error

	if c.Threshold < 0 || c.Threshold > 1 {
		problems = append(problems, fmt.Errorf("scoring.threshold は0〜1にしてください (現在 %v)", c.Threshold))
	}
	if c.ExactTextMatchConfidence < 0 || c.ExactTextMatchConfidence > 1 {
		problems = append(problems, fmt.Errorf("scoring.exact_text_match_confidence は0〜1にしてください (現在 %v)", c.ExactTextMatchConfidence))
	}
	if c.TimeOfDayWeight < 0 || c.TimeOfDayWeight > 1 {
		problems = append(problems, fmt.Errorf("scoring.time_of_day_weight は0〜1にしてください (現在 %v)", c.TimeOfDayWeight))
	}
	if c.NeighborWindowMinutes < 0 {
		problems = append(problems, fmt.Errorf("scoring.neighbor_window_minutes は0以上にしてください (現在 %d)", c.NeighborWindowMinutes))
	}

	return problems
}

func validateRules(rules []Rule) []error {
	var problems []error

	for i, rule := range rules {
		if rule.When.IsEmpty() {
			// 条件の無いルールは全件に効いてしまう。書き間違いとみなす。
			problems = append(problems, fmt.Errorf("rules[%d].when に条件が1つもありません。全記録に適用されてしまうため拒否します", i))
		}
		if rule.Confidence != nil && (*rule.Confidence < 0 || *rule.Confidence > 1) {
			problems = append(problems, fmt.Errorf("rules[%d].confidence は0〜1にしてください (現在 %v)", i, *rule.Confidence))
		}
		if rule.NeverSuggest && len(rule.Suggest) > 0 {
			problems = append(problems, fmt.Errorf("rules[%d] は never_suggest と suggest を同時に指定しています", i))
		}
		if !rule.NeverSuggest && len(rule.Suggest) == 0 && len(rule.CandidateTags) == 0 {
			problems = append(problems, fmt.Errorf("rules[%d] は suggest / candidate_tags / never_suggest のいずれも指定していません", i))
		}
	}

	return problems
}

// IsLoopbackURL は URL の接続先がループバックかを返す。
//
// ホスト名 "localhost" は名前解決の結果によらずループバック扱いにする。
// 実際に他所へ向く細工をされた環境まで守る意図はなく、
// うっかり外部のエンドポイントを書いた事故を止めるための関門である。
func IsLoopbackURL(raw string) (bool, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false, fmt.Errorf("error at parse url: %w", err)
	}
	host := parsed.Hostname()
	if host == "" {
		return false, errors.New("ホスト名がありません")
	}
	return isLoopbackHost(host), nil
}

// IsLoopbackListenAddr は "host:port" 形式の bind 先がループバックかを返す。
//
// ホストを省略した ":9797" は全インターフェースに開くのでループバックとしない。
func IsLoopbackListenAddr(addr string) (bool, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false, fmt.Errorf("error at split host port: %w", err)
	}
	if port == "" {
		return false, errors.New("ポート番号がありません")
	}
	if host == "" {
		// ":9797" のようにホストを省いた形。全インターフェースに開くので拒否する。
		return false, nil
	}
	return isLoopbackHost(host), nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// 名前解決が要るホスト名。ループバックとは認めない。
		return false
	}
	return ip.IsLoopback()
}

// ParseThumbSize はサムネ指定を解釈する。
//
// 書式と範囲は gkill 側の実装に合わせてある。gkill は範囲外を渡されても
// エラーにせず原本を返すため、意図せず全画素を読み込まないようこちらで弾く。
func ParseThumbSize(s string) (width int, height int, ok bool) {
	matched := thumbSizeRe.FindStringSubmatch(s)
	if matched == nil {
		return 0, 0, false
	}
	width, err := strconv.Atoi(matched[1])
	if err != nil {
		return 0, 0, false
	}
	height, err = strconv.Atoi(matched[2])
	if err != nil {
		return 0, 0, false
	}
	if width < 1 || height < 1 || width > maxThumbSide || height > maxThumbSide {
		return 0, 0, false
	}
	return width, height, true
}
