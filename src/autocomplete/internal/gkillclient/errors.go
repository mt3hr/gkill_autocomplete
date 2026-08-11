package gkillclient

import (
	"errors"
	"fmt"
	"strings"
)

// gkill のエラーコード。
const (
	// ErrCodeAccountNotFound はアカウントが見つからない。
	ErrCodeAccountNotFound = "ERR000002"
	// ErrCodeSessionNotFound はセッションが見つからない。
	ErrCodeSessionNotFound = "ERR000013"
	// ErrCodeAccountDisabled はアカウントが無効。
	ErrCodeAccountDisabled = "ERR000238"
	// ErrCodeSessionExpired はセッションの期限切れ。
	//
	// gkill 同梱の MCP 実装はこのコードを再ログインの契機に含めていない。
	// こちらでは必ず含める。含めないと、期限を跨いだ瞬間に
	// 「取り直せば通るのに落ちる」ことになる。
	ErrCodeSessionExpired = "ERR000373"
	// ErrCodeAlreadyExistTag は同じIDのタグが既にある。
	//
	// 決定的なIDを使うので、再実行時と「手で消したタグを蘇らせない」場面で
	// 必ず出る。失敗ではなく「何もしなかった」として扱う。
	ErrCodeAlreadyExistTag = "ERR000056"
)

// authErrorCodes は再ログインを試す価値のあるエラー。
var authErrorCodes = map[string]struct{}{
	ErrCodeAccountNotFound: {},
	ErrCodeSessionNotFound: {},
	ErrCodeAccountDisabled: {},
	ErrCodeSessionExpired:  {},
}

// APIError は gkill が返した業務エラー。
type APIError struct {
	Path   string
	Errors []*GkillError
}

func (e *APIError) Error() string {
	formatted := make([]string, 0, len(e.Errors))
	for _, gkillError := range e.Errors {
		if gkillError == nil {
			continue
		}
		formatted = append(formatted, gkillError.ErrorCode+": "+gkillError.ErrorMessage)
	}
	return fmt.Sprintf("gkill がエラーを返しました (%s): %s", e.Path, strings.Join(formatted, " / "))
}

// HasCode は指定のエラーコードが含まれるかを返す。
func (e *APIError) HasCode(code string) bool {
	for _, gkillError := range e.Errors {
		if gkillError != nil && gkillError.ErrorCode == code {
			return true
		}
	}
	return false
}

// hasAuthError は再ログインで解決しうるエラーが含まれるかを返す。
func hasAuthError(gkillErrors []*GkillError) bool {
	for _, gkillError := range gkillErrors {
		if gkillError == nil {
			continue
		}
		if _, ok := authErrorCodes[gkillError.ErrorCode]; ok {
			return true
		}
	}
	return false
}

// asAPIError はエラー配列が空でなければ APIError にして返す。
func asAPIError(path string, gkillErrors []*GkillError) error {
	nonNil := make([]*GkillError, 0, len(gkillErrors))
	for _, gkillError := range gkillErrors {
		if gkillError != nil {
			nonNil = append(nonNil, gkillError)
		}
	}
	if len(nonNil) == 0 {
		return nil
	}
	return &APIError{Path: path, Errors: nonNil}
}

// IsAlreadyExistTag は「同じIDのタグが既にある」エラーかを返す。
func IsAlreadyExistTag(err error) bool {
	apiError := &APIError{}
	if !errors.As(err, &apiError) {
		return false
	}
	return apiError.HasCode(ErrCodeAlreadyExistTag)
}
