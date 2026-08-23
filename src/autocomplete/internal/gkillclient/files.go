package gkillclient

// 編集前に読む: .claude/skills/autocomplete-gkill-client/SKILL.md（この領域の不変条件の正本）

import (
	"bytes"
	"context"
	"errors"
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

// ErrNotAnImage は画像でないものが返ってきたことを表す。
//
// **gkill の ?thumb= は画像以外のファイルにエラーを返さない。**
// 拡張子がサムネイルの対象でなければ、サムネイル生成を素通りして
// 原本をそのまま 200 で返す。動画・書庫・書類のいずれでも起きる。
// 素通しにすると、動画1本を maxImageBytes まで読み込むことになる。
var ErrNotAnImage = errors.New("gkill が画像でないものを返しました")

// ErrUnsupportedImageFormat は視覚モデルが復号できない画像形式を表す。
//
// **gkill が画像として返してきても、視覚モデルが読めるとは限らない。**
// gkill は拡張子で画像かを決めるので、RAW(.cr2)や .webp も image/* で返る。
// llama.cpp の復号器(stb_image)が扱えるのは JPEG / PNG / GIF / BMP だけで、
// それ以外を送ると復号に失敗し「LLM がエラーを返した」という
// 環境の問題に見える形で返ってくる。
// これは記録側の性質で、何度やり直しても結果は変わらない。
var ErrUnsupportedImageFormat = errors.New("視覚モデルが復号できない画像形式です")

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

	contentType := response.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	// **画像でなければ本文を読まない。**
	// ?thumb= は画像以外に対してエラーを返さず原本をそのまま寄越すので、
	// 読み切ると動画や書類の全量をメモリに載せることになる。
	// ファイル名は伏せる(エラーはログにも出るため)。
	if !strings.HasPrefix(contentType, "image/") {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxErrorBodyBytes))
		return Image{}, http.StatusOK, fmt.Errorf(
			"%w (Content-Type %q)。?thumb= は画像以外のファイルにはエラーを返さず、"+
				"サムネイルを作らずに原本をそのまま返します", ErrNotAnImage, contentType)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxImageBytes))
	if err != nil {
		return Image{}, 0, fmt.Errorf("error at read file body: %w", err)
	}

	// **Content-Type だけでは足りない。中身のバイト列も見る。**
	// gkill は RAW(.cr2)や .webp も画像として image/* で返すが、
	// 視覚モデル側(llama.cpp の stb_image)はこれらを復号できない。
	// 送ってしまうと「LLM がエラーを返した」という環境の問題に見える形で
	// 失敗し、実際には永久に判定できない記録のために毎回やり直すことになる。
	// 2026-08-16 の実データで .cr2 が 7,627件、.webp が 1,446件あり、
	// 固まって並んでいるため連続失敗で解析が打ち切られた。
	if !isDecodableImage(body) {
		return Image{}, http.StatusOK, fmt.Errorf(
			"%w (Content-Type %q)。視覚モデルが復号できるのは JPEG / PNG / GIF / BMP だけです",
			ErrUnsupportedImageFormat, contentType)
	}

	return Image{Bytes: body, ContentType: contentType}, http.StatusOK, nil
}

// isDecodableImage は視覚モデルが復号できる形式かを先頭バイトで判定する。
//
// 拡張子でも Content-Type でもなく実体を見る。gkill は拡張子で画像かを決めており、
// RAW も画像として image/* で返してくるため、型情報は当てにならない。
// 見るのは先頭の数バイトだけ。長さで足切りはしない
// (短いものを弾く判定にすると、短い正当なヘッダを持つ検査用の画像まで落ちる)。
func isDecodableImage(body []byte) bool {
	switch {
	case bytes.HasPrefix(body, []byte{0xFF, 0xD8}): // JPEG
		return true
	case bytes.HasPrefix(body, []byte{0x89, 'P', 'N', 'G'}): // PNG
		return true
	case bytes.HasPrefix(body, []byte("GIF8")): // GIF
		return true
	case bytes.HasPrefix(body, []byte("BM")): // BMP
		return true
	}
	return false
}
