// Package ids は提案とタグの識別子を決める。
//
// 識別子は (対象の記録ID, タグ名) から決定的に導く。乱数を使わないので、
// 何度解析し直しても同じ組み合わせには同じIDが割り当たる。これが
// 次の2つを同時に成立させている。
//
//   - 再解析しても提案が重複しない
//   - 一度却下した提案が復活しない(却下の記録が同じIDで残っているため)
//
// gkill 側の add_tag も、既に同じIDのタグがあれば追加せずエラーコードを返す。
// つまり「手で消したタグ」も同じIDのまま残るので、こちらから蘇らせてしまう
// ことがない。
package ids

import (
	"github.com/google/uuid"
)

// 名前空間。用途ごとに分けておかないと、同じ (対象ID, タグ名) から
// 提案IDとタグIDに同じ値が出てしまう。
var (
	suggestionNamespace = uuid.NewSHA1(uuid.NameSpaceOID, []byte("github.com/mt3hr/gkill_autocomplete/suggestion"))
	tagNamespace        = uuid.NewSHA1(uuid.NameSpaceOID, []byte("github.com/mt3hr/gkill_autocomplete/tag"))
)

// separator は対象IDとタグ名の境界。
//
// 区切りが無いと ("ab","c") と ("a","bc") が同じIDになってしまう。
// タグ名にも記録IDにも現れない文字を使う。
const separator = "\x00"

// SuggestionID は提案の識別子を返す。
func SuggestionID(targetID string, tagName string) string {
	return uuid.NewSHA1(suggestionNamespace, []byte(targetID+separator+tagName)).String()
}

// TagID は gkill に書き込むタグの識別子を返す。
func TagID(targetID string, tagName string) string {
	return uuid.NewSHA1(tagNamespace, []byte(targetID+separator+tagName)).String()
}
