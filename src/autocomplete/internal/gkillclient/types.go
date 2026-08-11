package gkillclient

import (
	"encoding/json"
	"time"
)

// GkillError は gkill が返すエラー1件。
//
// gkill は業務エラーを HTTP 200 のレスポンスボディに入れて返すので、
// HTTP ステータスだけを見て成功と判断してはいけない。
type GkillError struct {
	ErrorCode    string `json:"error_code"`
	ErrorMessage string `json:"error_message"`
}

// GkillMessage は gkill が返すメッセージ1件。
type GkillMessage struct {
	MessageCode string `json:"message_code"`
	Message     string `json:"message"`
}

// responseEnvelope は全レスポンスに共通する部分。
//
// 成功時の errors は空配列ではなく null で返る(Go 側の構造体タグに
// omitempty が無く、nil スライスがそのまま出るため)。
type responseEnvelope struct {
	Messages []*GkillMessage `json:"messages"`
	Errors   []*GkillError   `json:"errors"`
}

// FindQuery は gkill の検索条件。
//
// 重要: スライス項目は nil と空配列で意味が変わる。
//   - nil (JSON の null / キー欠落) = そのフィルタを使わない
//   - 非nilの空配列 [] = フィルタは有効だが該当0件
//
// Go の構造体では omitempty を付けて「未設定なら送らない」を徹底し、
// 意図せず0件指定になる事故を避ける。
type FindQuery struct {
	RepTypes          []string   `json:"rep_types,omitempty"`
	Reps              []string   `json:"reps,omitempty"`
	IDs               []string   `json:"ids,omitempty"`
	Words             []string   `json:"words,omitempty"`
	WordsAnd          bool       `json:"words_and,omitempty"`
	NotWords          []string   `json:"not_words,omitempty"`
	Tags              []string   `json:"tags,omitempty"`
	TagsAnd           bool       `json:"tags_and,omitempty"`
	CalendarStartDate *time.Time `json:"calendar_start_date,omitempty"`
	CalendarEndDate   *time.Time `json:"calendar_end_date,omitempty"`
	OnlyLatestData    bool       `json:"only_latest_data,omitempty"`
}

// GetKyousMCPRequest は POST /api/get_kyous_mcp のリクエスト。
//
// 通常の /api/get_kyous ではなくこちらを使う。/api/get_kyous が返す構造体は
// 15項目だけで本文もタイトルもタグも含まず、ページング機構も無いため、
// 記録1件ごとに追加のリクエストが要る形になってしまう。
type GetKyousMCPRequest struct {
	SessionID       string     `json:"session_id"`
	Query           *FindQuery `json:"query"`
	LocaleName      string     `json:"locale_name"`
	Limit           int        `json:"limit"`
	Cursor          string     `json:"cursor,omitempty"`
	MaxSizeMB       float64    `json:"max_size_mb"`
	IsIncludeTimeIs *bool      `json:"is_include_timeis"`
	IncludeID       bool       `json:"include_id"`
}

// GetKyousMCPResponse は POST /api/get_kyous_mcp のレスポンス。
type GetKyousMCPResponse struct {
	responseEnvelope
	Kyous         []Kyou `json:"kyous"`
	TotalCount    int    `json:"total_count"`
	ReturnedCount int    `json:"returned_count"`
	HasMore       bool   `json:"has_more"`
	NextCursor    string `json:"next_cursor"`
}

// Kyou は gkill の記録1件。
type Kyou struct {
	ID          string          `json:"id"`
	DataType    string          `json:"data_type"`
	RelatedTime time.Time       `json:"related_time"`
	Tags        []string        `json:"tags"`
	Texts       []string        `json:"texts"`
	Payload     json.RawMessage `json:"payload"`
}

// Payload は記録の種別ごとの中身。
//
// 種別ごとに別々の型で返ってくるが、項目名は重複しないので
// すべてを持つ1つの構造体に流し込める。どの項目が有効かは Kind で判る。
type Payload struct {
	Kind string `json:"kind"`

	// kmemo
	Content string `json:"content"`

	// kc / urlog / nlog / mi / timeis に共通
	Title string `json:"title"`

	// urlog
	URL string `json:"url"`

	// nlog
	Shop   string  `json:"shop"`
	Amount float64 `json:"amount"`

	// kc
	NumValue float64 `json:"num_value"`

	// lantana
	Mood int `json:"mood"`

	// idf
	FileName string `json:"file_name"`
	IsImage  bool   `json:"is_image"`
	IsVideo  bool   `json:"is_video"`
	IsAudio  bool   `json:"is_audio"`
	RepName  string `json:"rep_name"`
	MimeType string `json:"mime_type"`
	FilePath string `json:"file_path"`

	// git_commit_log
	CommitMessage string `json:"commit_message"`

	// plugin
	PluginName string `json:"plugin_name"`
}

// DecodePayload は記録の Payload を解釈する。
// Payload が無い記録では空の Payload とともに false を返す。
func (k Kyou) DecodePayload() (Payload, bool) {
	if len(k.Payload) == 0 {
		return Payload{}, false
	}
	decoded := Payload{}
	if err := json.Unmarshal(k.Payload, &decoded); err != nil {
		return Payload{}, false
	}
	return decoded, true
}

// getAllTagNamesRequest は POST /api/get_all_tag_names のリクエスト。
type getAllTagNamesRequest struct {
	SessionID  string `json:"session_id"`
	LocaleName string `json:"locale_name"`
}

// getAllTagNamesResponse は POST /api/get_all_tag_names のレスポンス。
type getAllTagNamesResponse struct {
	responseEnvelope
	TagNames []string `json:"tag_names"`
}

// getAllRepNamesResponse は POST /api/get_all_rep_names のレスポンス。
// リクエストは get_all_tag_names と同じ形なので使い回す。
type getAllRepNamesResponse struct {
	responseEnvelope
	RepNames []string `json:"rep_names"`
}

// Tag は記録に付けるタグ。
//
// ID と各時刻はサーバが補完しないので、すべて呼び出し側が入れる。
// RelatedTime を省くとゼロ値(0001-01-01)になり、時系列の表示から外れてしまう。
type Tag struct {
	IsDeleted    bool      `json:"is_deleted"`
	ID           string    `json:"id"`
	TargetID     string    `json:"target_id"`
	RelatedTime  time.Time `json:"related_time"`
	CreateTime   time.Time `json:"create_time"`
	CreateApp    string    `json:"create_app"`
	CreateDevice string    `json:"create_device"`
	CreateUser   string    `json:"create_user"`
	UpdateTime   time.Time `json:"update_time"`
	UpdateApp    string    `json:"update_app"`
	UpdateDevice string    `json:"update_device"`
	UpdateUser   string    `json:"update_user"`
	Tag          string    `json:"tag"`
	RepName      string    `json:"rep_name"`
}

// addTagRequest は POST /api/add_tag のリクエスト。
type addTagRequest struct {
	SessionID  string  `json:"session_id"`
	Tag        Tag     `json:"tag"`
	TXID       *string `json:"tx_id"`
	LocaleName string  `json:"locale_name"`
}

// addTagResponse は POST /api/add_tag のレスポンス。
type addTagResponse struct {
	responseEnvelope
	AddedTag *Tag `json:"added_tag"`
}
