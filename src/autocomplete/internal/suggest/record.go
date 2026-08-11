// Package suggest は履歴からタグの付け方を学び、まだタグの付いていない
// 記録に対して付けるべきタグを提案する。
//
// 設計の要点は3つ。
//
//  1. 候補タグごとに独立して yes/no を出す(multi-label)。最も確からしい1つを
//     選ぶのではないので、結果は0個にも複数個にもなる。0個は「タグを付けない」
//     という正常な答えであって、失敗ではない。実際、記録の一定割合は
//     意図的にタグを付けないまま残される。
//
//  2. 判定は3段階で、上で決まれば下へ行かない。本文が過去の記録と逐語一致
//     するなら、それ以上何かを推測する必要はない。LLM は最後の手段。
//
//  3. 特定のタグ名をコードに書かない。候補タグは履歴での実績から決まる。
//     人が決め打ちしたいときは設定のルールで上書きする。
package suggest

import (
	"strings"
	"time"

	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/gkillclient"
)

// Record は判定の対象となる記録。
//
// gkill から取れる情報のうち、判定に使う分だけを持つ。
// 位置情報やファイルの絶対パスは持たない(LLM へ渡さないため)。
type Record struct {
	ID          string
	DataType    string
	RelatedTime time.Time
	Tags        []string

	// Title は金銭記録の品名など、短い見出し。無い種別では空。
	Title string
	// Text は本文。判定に使う文字列をまとめたもの。
	Text string

	// RepName と RepFamily は写真の記録でのみ埋まる。
	// gkill の一覧取得は写真以外にリポジトリ名を返さないため。
	RepName   string
	RepFamily string

	// IsImage が真のとき、写真として判定できる。
	IsImage  bool
	FileName string
}

// FromKyou は gkill の記録を判定用の形に落とす。
func FromKyou(kyou gkillclient.Kyou) Record {
	record := Record{
		ID:          kyou.ID,
		DataType:    kyou.DataType,
		RelatedTime: kyou.RelatedTime,
		Tags:        kyou.Tags,
	}

	parts := make([]string, 0, len(kyou.Texts)+2)

	payload, ok := kyou.DecodePayload()
	if ok {
		record.Title = payload.Title
		record.RepName = payload.RepName
		record.RepFamily = RepFamilyOf(payload.RepName)
		record.IsImage = payload.IsImage
		record.FileName = payload.FileName

		for _, candidate := range []string{
			payload.Title,
			payload.Content,
			payload.Shop,
			payload.URL,
			payload.CommitMessage,
		} {
			if strings.TrimSpace(candidate) != "" {
				parts = append(parts, candidate)
			}
		}
	}

	// 記録に付けられた本文(テキスト)も判定材料にする。
	for _, text := range kyou.Texts {
		if strings.TrimSpace(text) != "" {
			parts = append(parts, text)
		}
	}

	record.Text = strings.Join(parts, "\n")
	return record
}

// RepFamilyOf はリポジトリ名から種別の部分を取り出す。
//
// gkill のリポジトリ名は「種別_端末_日付」の形をしているので、
// 先頭の区間が「同じ種類のリポジトリ」を表す。端末を買い替えても
// 同じ family に落ちるので、学習の文脈として使える。
func RepFamilyOf(repName string) string {
	trimmed := strings.TrimSpace(repName)
	if trimmed == "" {
		return ""
	}
	family, _, found := strings.Cut(trimmed, "_")
	if !found {
		return trimmed
	}
	return family
}

// ContextKey は学習と候補抽出の単位を返す。
//
// 写真はリポジトリの種別ごとに写すものが決まっているので、そちらを文脈にする。
// リポジトリ名が取れない種別では記録の種別そのものを文脈にする。
func (r Record) ContextKey() string {
	if r.RepFamily != "" {
		return "rep:" + r.RepFamily
	}
	return "type:" + r.DataType
}

// HasAnyTag はタグが1つでも付いているかを返す。
func (r Record) HasAnyTag() bool {
	return len(r.Tags) > 0
}

// NormalizeText は本文を照合用に正規化する。
//
// 同じ操作を繰り返したときに生まれる定型の記録は、字面がほぼ完全に一致する。
// 前後の空白と改行の揺れ、英字の大小だけを吸収すれば、逐語一致の照合に足りる。
func NormalizeText(text string) string {
	replaced := strings.NewReplacer("\r\n", "\n", "\r", "\n", "　", " ", "\t", " ").Replace(text)

	lines := strings.Split(replaced, "\n")
	normalizedLines := make([]string, 0, len(lines))
	for _, line := range lines {
		collapsed := strings.Join(strings.Fields(line), " ")
		if collapsed == "" {
			continue
		}
		normalizedLines = append(normalizedLines, collapsed)
	}
	return strings.ToLower(strings.Join(normalizedLines, "\n"))
}
