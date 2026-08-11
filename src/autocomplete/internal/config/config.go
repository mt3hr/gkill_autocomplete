// Package config は gkill_autocomplete の設定の読み込みと検証を担う。
//
// このパッケージは「生活の記録を外へ出さない」という本アプリの最優先制約を
// 実装する場所でもある。LLM の接続先とWebサーバのbind先がループバックに
// 限られていることの検証は Validate に閉じており、他の場所で判定しない。
package config

import (
	"encoding/json"
	"fmt"
	"time"
)

// Config は設定ファイル全体。
//
// キー名が "_" で始まる項目は読み飛ばされる(コメントとして書ける)。
// それ以外の未知のキーは、書いたのに効かない設定を黙って見逃さないため
// エラーにする。
type Config struct {
	Server     ServerConfig     `json:"server"`
	Gkill      GkillConfig      `json:"gkill"`
	LLM        LLMConfig        `json:"llm"`
	Scope      ScopeConfig      `json:"scope"`
	Exclude    ExcludeConfig    `json:"exclude"`
	Candidates CandidatesConfig `json:"candidates"`
	Scoring    ScoringConfig    `json:"scoring"`
	Rules      []Rule           `json:"rules"`
}

// ServerConfig は確認画面を配信するWebサーバの設定。
type ServerConfig struct {
	// Listen は bind するアドレス。
	//
	// ループバック以外へ開いてよい。画面は gkill のアカウントによる
	// ログインで守られ、通信は gkill と同じ証明書で暗号化されるため。
	Listen string `json:"listen"`
}

// GkillConfig は稼働中の gkill サーバへの接続設定。
//
// **資格情報はここに書かない。** 認証は gkill の設定ディレクトリを
// 直接見て行うので、パスワードもそのハッシュも持たない。
type GkillConfig struct {
	// Home は gkill のホームディレクトリ。
	//
	// 空なら $GKILL_HOME、それも空なら $HOME/gkill を使う。
	// ここから account.db / account_state.db / server_config.db を見つける。
	Home string `json:"home"`

	// BaseURL は gkill 本体の宛先。
	//
	// 空なら server_config.db から組み立てる。gkill が TLS を使っているかも
	// 待ち受けポートもそちらに書いてあるので、**通常は空のままでよい**。
	// 手で書くと、gkill 側の設定を変えたときに食い違って繋がらなくなる。
	BaseURL string `json:"base_url"`

	LocaleName string `json:"locale_name"`

	// InsecureSkipVerify は自己署名証明書のローカル gkill に繋ぐとき用。
	//
	// gkill が作る証明書は localhost 向けの自己署名なので、
	// 既定で真にしてある。gkill 本体も MCP サーバも同じことをしている。
	InsecureSkipVerify bool `json:"insecure_skip_verify"`

	TimeoutSeconds int `json:"timeout_seconds"`
}

// Timeout は TimeoutSeconds を time.Duration にして返す。
func (c GkillConfig) Timeout() time.Duration {
	return time.Duration(c.TimeoutSeconds) * time.Second
}

// LLMConfig は判定に使うローカルLLMの設定。
type LLMConfig struct {
	// Endpoint は OpenAI 互換の /v1/chat/completions。
	// ループバック以外を指定した場合、AllowRemote が真でなければ起動を拒否する。
	Endpoint string `json:"endpoint"`

	// AllowRemote はループバック制限を解除する。
	// 生活の記録の本文と写真が外部へ出るので、既定では決して有効にしない。
	AllowRemote bool `json:"allow_remote"`

	TextModel   string `json:"text_model"`
	VisionModel string `json:"vision_model"`

	TimeoutSeconds int `json:"timeout_seconds"`

	// ThumbSize は gkill に要求するサムネイルの大きさ。"400x400" の形式。
	// 各辺 1〜1024 の範囲外だと gkill は原本(全画素)を返してしまうので、
	// Validate で範囲を検査する。
	ThumbSize string `json:"thumb_size"`
}

// Timeout は TimeoutSeconds を time.Duration にして返す。
func (c LLMConfig) Timeout() time.Duration {
	return time.Duration(c.TimeoutSeconds) * time.Second
}

// ScopeConfig は解析の対象範囲。
//
// 無制限に走らせると他のツールが自動収集した記録まで拾ってしまうので必ず絞る。
type ScopeConfig struct {
	// RepPrefixes が空のときは「手作業でタグを付けている割合が高いリポジトリ」を
	// 自動で選ぶ。実在のリポジトリ名を既定値としてリポジトリに焼き込まないための既定。
	RepPrefixes []string `json:"rep_prefixes"`

	// DataTypes が空のときは全種別が対象。
	DataTypes []string `json:"data_types"`

	CandidateDays int `json:"candidate_days"`
	LearnDays     int `json:"learn_days"`

	// MaxScanRecords は1回の解析で読む記録数の上限。
	//
	// 範囲を絞らずに走らせたときの暴走を止めるための歯止め。
	// ここに達すると学習が不完全なまま終わるので、達したことは警告で知らせる。
	MaxScanRecords int `json:"max_scan_records"`
}

// ExcludeConfig は提案の対象から外すものの指定。
type ExcludeConfig struct {
	// TagPatterns は末尾 "*" のワイルドカードを解する前方一致パターン。
	// 他のツールが機械的に付けたタグを候補から外すために使う。
	TagPatterns []string `json:"tag_patterns"`

	// AlreadyTagged が真のとき、タグが1つでも付いている記録は対象外。
	AlreadyTagged bool `json:"already_tagged"`
}

// CandidatesConfig は候補タグの絞り込み。
type CandidatesConfig struct {
	// MinExamples は候補タグに必要な履歴での実績件数。
	MinExamples int `json:"min_examples"`

	// MaxCandidateTags は LLM に一度に問う候補タグの上限。
	MaxCandidateTags int `json:"max_candidate_tags"`

	// MaxFewShotExamples は候補タグ1つあたりに添える参考例の数。
	MaxFewShotExamples int `json:"max_few_shot_examples"`

	// MaxFewShotImages は1回の問い合わせで LLM に見せる見本写真の総数。
	//
	// 写真は文字よりはるかに嵩み、1枚で千数百トークンになる。
	// LLM の文脈長が既定の 4096 のままだと、数枚添えるだけで溢れる。
	// 0 にすると見本を添えない。
	MaxFewShotImages int `json:"max_few_shot_images"`
}

// ScoringConfig は確信度の合成と足切り。
//
// 判定は候補タグごとに独立なので、結果は0個にも複数個にもなる。
// 0個は「タグを付けない」という正常な答えであって、失敗ではない。
type ScoringConfig struct {
	Threshold                float64 `json:"threshold"`
	ExactTextMatchConfidence float64 `json:"exact_text_match_confidence"`
	NeighborWindowMinutes    int     `json:"neighbor_window_minutes"`
	TimeOfDayWeight          float64 `json:"time_of_day_weight"`
}

// NeighborWindow は NeighborWindowMinutes を time.Duration にして返す。
func (c ScoringConfig) NeighborWindow() time.Duration {
	return time.Duration(c.NeighborWindowMinutes) * time.Minute
}

// Rule は手書きの上書きルール。
//
// 学習だけで足りないときに人が書く。設定だけで挙動が変わり、
// コードに特定のタグ名を書かなくて済むようにするためのもの。
type Rule struct {
	When RuleWhen `json:"when"`

	// CandidateTags は候補タグ集合を固定する(学習結果を置き換える)。
	CandidateTags []string `json:"candidate_tags"`

	// Suggest は条件に合致したとき決め打ちで提案するタグ。
	Suggest []string `json:"suggest"`

	// Confidence は Suggest に与える確信度。nil なら既定値を使う。
	Confidence *float64 `json:"confidence"`

	// NeverSuggest が真のとき、When に合致するタグは決して提案しない。
	NeverSuggest bool `json:"never_suggest"`
}

// RuleWhen はルールの適用条件。指定した項目がすべて合致したとき成立する。
// 何も指定しないルールは Validate が拒否する(全件に効いて事故になるため)。
type RuleWhen struct {
	RepPrefix             string `json:"rep_prefix"`
	DataType              string `json:"data_type"`
	TextEquals            string `json:"text_equals"`
	TextContains          string `json:"text_contains"`
	NeighborTitleContains string `json:"neighbor_title_contains"`
	Tag                   string `json:"tag"`
}

// IsEmpty は条件が1つも指定されていないことを返す。
func (w RuleWhen) IsEmpty() bool {
	return w.RepPrefix == "" &&
		w.DataType == "" &&
		w.TextEquals == "" &&
		w.TextContains == "" &&
		w.NeighborTitleContains == "" &&
		w.Tag == ""
}

// Default は既定の設定を返す。
//
// ここに書いてよいのは「使い方の分析から決めた構造的な既定」だけで、
// 実在のタグ名やリポジトリ名を焼き込んではいけない。
// 利用者ごとの値は init が稼働中の gkill を解析して生成する。
func Default() Config {
	return Config{
		Server: ServerConfig{
			Listen: "127.0.0.1:9797",
		},
		Gkill: GkillConfig{
			// Home も BaseURL も空のままにする。
			// gkill の設定ディレクトリとサーバ設定から自動で決まるので、
			// 手で書かせると gkill 側を変えたときに食い違う。
			Home:       "",
			BaseURL:    "",
			LocaleName: "ja",
			// gkill が作る証明書は localhost 向けの自己署名なので、
			// 検証を通せない。gkill 本体も MCP サーバも同じ扱いにしている。
			InsecureSkipVerify: true,
			TimeoutSeconds:     120,
		},
		LLM: LLMConfig{
			Endpoint:    "http://127.0.0.1:11434/v1/chat/completions",
			AllowRemote: false,
			// 写真の判定は1件で10分近くかかることがある(CPU で動かした場合)。
			// 短く切ると、写真の記録だけが毎回時間切れで飛ばされ続ける。
			// 解析はリクエストとは別に走るので、長くしても画面は待たない。
			TimeoutSeconds: 900,
			ThumbSize:      "400x400",
		},
		Scope: ScopeConfig{
			RepPrefixes: []string{},
			DataTypes:   []string{},
			// 滞留は2日ぶん程度なので30日あれば取りこぼさない。
			CandidateDays: 30,
			// 季節で記録の傾向が変わるので学習範囲は長めに取る。
			LearnDays: 180,
			// 自動収集の記録まで含めると日に数百件生まれる。
			// 範囲を絞らない使い方でも破綻しないところに置く。
			MaxScanRecords: 60000,
		},
		Exclude: ExcludeConfig{
			TagPatterns:   []string{},
			AlreadyTagged: true,
		},
		Candidates: CandidatesConfig{
			MinExamples:        5,
			MaxCandidateTags:   8,
			MaxFewShotExamples: 4,
			// 写真は1枚で千数百トークンになる。文脈長が既定の 4096 のままの
			// 環境でも収まるよう、控えめにしておく。
			MaxFewShotImages: 2,
		},
		Scoring: ScoringConfig{
			Threshold: 0.6,
			// 同一文面の繰り返しに同じタグが付く例は極めて強い手がかりなので高く取る。
			ExactTextMatchConfidence: 0.95,
			// 関連する記録どうしは数秒〜数分の差で並ぶ。
			NeighborWindowMinutes: 5,
			// 時刻の偏りは弱い手がかりでしかないので小さく。
			TimeOfDayWeight: 0.1,
		},
		Rules: []Rule{},
	}
}

// Redacted は伏せるべき値を隠した複製を返す。
//
// いまの設定は資格情報を持たない(認証は gkill の設定ディレクトリを
// 直接見て行う)ので伏せるものは無いが、**出力の入口をここ1箇所に
// 保っておく**ために残してある。秘密を持つ項目が将来増えたとき、
// 「出力する側を全部直す」ことにならないようにするため。
func (c Config) Redacted() Config {
	return c
}

// MarshalIndentRedacted は伏せるべき値を隠した上で整形済みJSONにする。
func (c Config) MarshalIndentRedacted() ([]byte, error) {
	marshaled, err := json.MarshalIndent(c.Redacted(), "", "  ")
	if err != nil {
		return nil, fmt.Errorf("error at marshal config: %w", err)
	}
	return marshaled, nil
}
