package gkillclient

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// FetchOptions は記録の取得条件。
type FetchOptions struct {
	// PageLimit は1回のリクエストで取る件数。0 なら既定値。
	PageLimit int

	// MaxSizeMB は1回のレスポンスの上限サイズ。0 なら既定値。
	MaxSizeMB float64

	// MaxTotal は取得する総件数の上限。0 なら無制限。
	MaxTotal int

	// IncludeID は記録のIDを含めるか。
	// タグを付けるには対象のIDが要るので、通常は真にする。
	IncludeID bool
}

const (
	// defaultPageLimit は1回のリクエストで取る件数。
	//
	// gkill 側は1回につき最大1000件しか返さない(1000を超える値を送っても1000で頭打ち)。
	// その上限いっぱいを取る。
	//
	// ここを小さくすると往復が増え、往復ごとに検索が走り直すので、
	// 総取得時間がそのまま倍々になる。実測では3万件を200件刻みで取ると
	// 153往復・30分超かかり、1000件刻みなら31往復で済んだ。
	defaultPageLimit = 1000

	// defaultMaxSizeMB は1回のレスポンスの上限サイズ。
	//
	// 1000件が1回に収まる大きさにしておく。小さすぎると件数の上限より先に
	// こちらで頭打ちになり、往復が増える。
	defaultMaxSizeMB = 16.0
)

// FetchKyous は条件に合う記録をすべて取る。
//
// /api/get_kyous ではなく /api/get_kyous_mcp を使う。
// /api/get_kyous が返す構造体は15項目だけで本文もタイトルもタグも含まず、
// ページング機構も無いため、記録1件ごとに追加のリクエストが必要になる。
// get_kyous_mcp なら tags / texts / payload が1回で揃う。
func (c *Client) FetchKyous(ctx context.Context, query *FindQuery, options FetchOptions) ([]Kyou, error) {
	pageLimit := options.PageLimit
	if pageLimit <= 0 {
		pageLimit = defaultPageLimit
	}
	maxSizeMB := options.MaxSizeMB
	if maxSizeMB <= 0 {
		maxSizeMB = defaultMaxSizeMB
	}

	// 添付 TimeIs は判定に使わないので取らない。レスポンスを小さく保つ。
	includeTimeIs := false

	collected := make([]Kyou, 0, pageLimit)
	cursor := ""

	for {
		if options.MaxTotal > 0 && len(collected) >= options.MaxTotal {
			break
		}

		requestLimit := pageLimit
		if options.MaxTotal > 0 {
			remaining := options.MaxTotal - len(collected)
			if remaining < requestLimit {
				requestLimit = remaining
			}
		}

		pageCursor := cursor
		raw, err := c.callAuthed(ctx, "/api/get_kyous_mcp", func(sessionID string) any {
			return GetKyousMCPRequest{
				SessionID:       sessionID,
				Query:           query,
				LocaleName:      c.localeName,
				Limit:           requestLimit,
				Cursor:          pageCursor,
				MaxSizeMB:       maxSizeMB,
				IsIncludeTimeIs: &includeTimeIs,
				IncludeID:       options.IncludeID,
			}
		})
		if err != nil {
			return nil, err
		}

		response := GetKyousMCPResponse{}
		if err := json.Unmarshal(raw, &response); err != nil {
			return nil, fmt.Errorf("error at parse get_kyous_mcp response: %w", err)
		}

		collected = append(collected, response.Kyous...)

		if !response.HasMore || response.NextCursor == "" {
			break
		}
		if response.NextCursor == cursor {
			// カーソルが進まない。無限ループを避けるために打ち切る。
			break
		}
		cursor = response.NextCursor
	}

	return collected, nil
}

// GetAllTagNames は定義済みのタグ名をすべて返す。
func (c *Client) GetAllTagNames(ctx context.Context) ([]string, error) {
	raw, err := c.callAuthed(ctx, "/api/get_all_tag_names", func(sessionID string) any {
		return getAllTagNamesRequest{
			SessionID:  sessionID,
			LocaleName: c.localeName,
		}
	})
	if err != nil {
		return nil, err
	}

	response := getAllTagNamesResponse{}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, fmt.Errorf("error at parse get_all_tag_names response: %w", err)
	}
	return response.TagNames, nil
}

// GetAllRepNames は利用者のリポジトリ名をすべて返す。
//
// 設定では扱いやすさのために接頭辞で範囲を指定するが、gkill の検索条件は
// リポジトリ名そのものを取るので、ここで実名に展開するのに使う。
func (c *Client) GetAllRepNames(ctx context.Context) ([]string, error) {
	raw, err := c.callAuthed(ctx, "/api/get_all_rep_names", func(sessionID string) any {
		return getAllTagNamesRequest{
			SessionID:  sessionID,
			LocaleName: c.localeName,
		}
	})
	if err != nil {
		return nil, err
	}

	response := getAllRepNamesResponse{}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, fmt.Errorf("error at parse get_all_rep_names response: %w", err)
	}
	return response.RepNames, nil
}

// NewTag はタグを組み立てる。
//
// ID と各時刻はサーバが補完しないので、すべてここで入れる。
// relatedTime には対象の記録の時刻を渡すこと。省くとゼロ値になり、
// 時系列の表示から外れてしまう。
func NewTag(id string, targetID string, tagName string, relatedTime time.Time, userID string, device string, now time.Time) Tag {
	return Tag{
		IsDeleted:    false,
		ID:           id,
		TargetID:     targetID,
		Tag:          tagName,
		RelatedTime:  relatedTime,
		CreateTime:   now,
		CreateApp:    AppName,
		CreateDevice: device,
		CreateUser:   userID,
		UpdateTime:   now,
		UpdateApp:    AppName,
		UpdateDevice: device,
		UpdateUser:   userID,
	}
}

// AddTag はタグを1つ付ける。
//
// 同じIDのタグが既にある場合は alreadyExist=true を返し、エラーにはしない。
// 決定的なIDを使っているので、これは次の2つの場面で必ず起きる。
//   - 同じ提案を二度承認した(何もしなくてよい)
//   - 過去に付けたあと手で消した(蘇らせてはいけない)
//
// どちらも「何もしなかった」が正しい結果なので、失敗として扱わない。
func (c *Client) AddTag(ctx context.Context, tag Tag) (alreadyExist bool, err error) {
	raw, err := c.callAuthed(ctx, "/api/add_tag", func(sessionID string) any {
		return addTagRequest{
			SessionID:  sessionID,
			Tag:        tag,
			TXID:       nil,
			LocaleName: c.localeName,
		}
	})
	if err != nil {
		if IsAlreadyExistTag(err) {
			return true, nil
		}
		return false, err
	}

	response := addTagResponse{}
	if err := json.Unmarshal(raw, &response); err != nil {
		return false, fmt.Errorf("error at parse add_tag response: %w", err)
	}
	return false, nil
}
