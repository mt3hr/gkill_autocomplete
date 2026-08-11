package gkillclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/config"
)

// maxImageBytes は1枚の画像を読む上限。
//
// サムネイル指定を誤ると gkill はエラーを返さず原本を返すので、
// 想定外に巨大な画像を丸ごと読み込まないための歯止め。
const maxImageBytes = 16 << 20

// BuildFileURL はリポジトリ名とファイル名から /files/ のパスを組む。
//
// gkill 側の組み立て方に合わせてある。ファイル名の中の "/" は区切りとして
// 残し、各区間だけをエスケープする。
func BuildFileURL(repName string, fileName string) string {
	rep := escapePathSegments(repName)
	rel := escapePathSegments(strings.ReplaceAll(fileName, "\\", "/"))

	switch {
	case rep == "" && rel == "":
		return "/files/"
	case rep == "":
		return "/files/" + rel
	case rel == "":
		return "/files/" + rep + "/"
	default:
		return "/files/" + rep + "/" + rel
	}
}

// escapePathSegments は "/" 区切りの各区間を URL エスケープする。
// 空の区間と "." ".." は落とす(パスを遡らせないため)。
func escapePathSegments(path string) string {
	segments := strings.Split(path, "/")
	escaped := make([]string, 0, len(segments))
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			continue
		}
		escaped = append(escaped, url.PathEscape(segment))
	}
	return strings.Join(escaped, "/")
}

// Image は取得した画像。
type Image struct {
	Bytes       []byte
	ContentType string
}

// FetchThumb はサムネイルを取得する。
//
// /files/ の認証はクッキーだけで、リクエストボディやクエリでは
// セッションを渡せない。
//
// thumbSize は "400x400" の形式で各辺 1〜1024。範囲外を渡すと gkill は
// エラーにせず原本(全画素)を返すので、送る前に検査する。
func (c *Client) FetchThumb(ctx context.Context, repName string, fileName string, thumbSize string) (Image, error) {
	if _, _, ok := config.ParseThumbSize(thumbSize); !ok {
		return Image{}, fmt.Errorf(
			"サムネイル指定が不正です (%q)。'400x400' の形式で各辺 1〜1024 にしてください。"+
				"範囲外だと gkill は原本(全画素)を返します", thumbSize)
	}

	requestURL := c.baseURL + BuildFileURL(repName, fileName) + "?thumb=" + url.QueryEscape(thumbSize)

	sessionID, err := c.EnsureSession(ctx)
	if err != nil {
		return Image{}, err
	}

	image, status, err := c.fetchFile(ctx, requestURL, sessionID)
	if err != nil {
		return Image{}, err
	}
	if status == http.StatusForbidden {
		// セッションが死んでいる可能性がある。取り直して1回だけやり直す。
		c.sessions.Invalidate(ctx)

		sessionID, err = c.EnsureSession(ctx)
		if err != nil {
			return Image{}, err
		}
		image, status, err = c.fetchFile(ctx, requestURL, sessionID)
		if err != nil {
			return Image{}, err
		}
	}
	if status != http.StatusOK {
		// URL には利用者のファイル名が入るのでメッセージに含めない。
		return Image{}, fmt.Errorf("gkill が画像の取得に HTTP %d を返しました (リポジトリ %q)", status, repName)
	}
	return image, nil
}

func (c *Client) fetchFile(ctx context.Context, requestURL string, sessionID string) (Image, int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return Image{}, 0, fmt.Errorf("error at make file request: %w", err)
	}
	// /files/ はクッキーでしか認証できない。
	request.AddCookie(&http.Cookie{Name: "gkill_session_id", Value: sessionID})

	response, err := c.httpClient.Do(request)
	if err != nil {
		return Image{}, 0, fmt.Errorf("error at request file: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		// 本文は読み捨てる。接続の再利用のために少しだけ吸う。
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxErrorBodyBytes))
		return Image{}, response.StatusCode, nil
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxImageBytes))
	if err != nil {
		return Image{}, 0, fmt.Errorf("error at read file body: %w", err)
	}

	contentType := response.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return Image{Bytes: body, ContentType: contentType}, http.StatusOK, nil
}
