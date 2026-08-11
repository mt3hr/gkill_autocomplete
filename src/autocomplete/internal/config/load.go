package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

const (
	// HomeEnvName は設定・データベース・ログの置き場を指す環境変数。
	HomeEnvName = "GKILL_AUTOCOMPLETE_HOME"

	// ConfigFileName は設定ファイル名。
	ConfigFileName = "config.json"

	// StoreFileName は提案と人間の判定を入れる SQLite。
	StoreFileName = "gkill_autocomplete.db"
)

// commentKeyPrefix で始まるキーは読み飛ばす。
// JSON にコメントが書けないので、設定ファイル自身に説明を書けるようにするための逃げ道。
const commentKeyPrefix = "_"

// Home は設定とデータの置き場を返す。
//
// 既定は $HOME/gkill_autocomplete。リポジトリの中には決して作らない。
func Home() (string, error) {
	if fromEnv := strings.TrimSpace(os.Getenv(HomeEnvName)); fromEnv != "" {
		return fromEnv, nil
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("error at get user home dir: %w", err)
	}
	return filepath.Join(userHome, "gkill_autocomplete"), nil
}

// ConfigPath は設定ファイルの既定の場所を返す。
func ConfigPath() (string, error) {
	home, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ConfigFileName), nil
}

// Load は設定ファイルを読んで検証済みの Config を返す。
//
// 未指定の項目は Default() の値になる。環境変数は設定ファイルより優先される
// (資格情報をファイルに書かずに済ませるため)。
func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("error at read config file %q: %w", path, err)
	}
	return Parse(raw)
}

// Parse は設定ファイルの中身から検証済みの Config を作る。
func Parse(raw []byte) (Config, error) {
	if err := CheckUnknownKeys(raw); err != nil {
		return Config{}, err
	}

	// 既定値を入れた構造体へ上書きしていく。
	// 書かれていないキーは既定のまま残り、書かれたキーだけが変わる。
	loaded := Default()
	if err := json.Unmarshal(raw, &loaded); err != nil {
		return Config{}, fmt.Errorf("error at parse config: %w", err)
	}

	applyEnvOverrides(&loaded)

	if err := Validate(loaded); err != nil {
		return Config{}, fmt.Errorf("設定に問題があります:\n%w", err)
	}
	return loaded, nil
}

// LoadOrDefault は設定ファイルがあれば読み、無ければ既定値を返す。
func LoadOrDefault(path string) (Config, error) {
	_, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		loaded := Default()
		applyEnvOverrides(&loaded)
		if err := Validate(loaded); err != nil {
			return Config{}, fmt.Errorf("既定の設定に問題があります:\n%w", err)
		}
		return loaded, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("error at stat config file %q: %w", path, err)
	}
	return Load(path)
}

// applyEnvOverrides は環境変数を設定に反映する。
//
// 環境変数のほうが設定ファイルより強い。
//
// **資格情報を受け取る経路はもう無い。** 認証は gkill の設定ディレクトリを
// 直接見て行うので、パスワードもそのハッシュも渡す必要が無くなった。
func applyEnvOverrides(target *Config) {
	if value := strings.TrimSpace(os.Getenv("GKILL_HOME")); value != "" {
		target.Gkill.Home = value
	}
	if value := strings.TrimSpace(os.Getenv("GKILL_BASE_URL")); value != "" {
		target.Gkill.BaseURL = value
	}
	if value := strings.TrimSpace(os.Getenv("GKILL_LOCALE")); value != "" {
		target.Gkill.LocaleName = value
	}
	if value := strings.TrimSpace(os.Getenv("GKILL_INSECURE")); value == "true" || value == "1" {
		target.Gkill.InsecureSkipVerify = true
	}
}

// Save は設定ファイルを書き出す。既存のファイルは上書きしない。
//
// 利用者が手で書いた設定を、生成処理が黙って消してしまうことを防ぐ。
func Save(path string, target Config) error {
	return save(path, target)
}

func save(path string, target Config) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("設定ファイルが既にあります: %s (上書きしません)", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("error at stat config file %q: %w", path, err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("error at make config dir: %w", err)
	}

	// Redacted は通さない。ここで書くのは利用者自身の設定ファイルであり、
	// 伏せ字を書き込んでしまうと動かなくなる。
	marshaled, err := json.MarshalIndent(target, "", "  ")
	if err != nil {
		return fmt.Errorf("error at marshal config: %w", err)
	}
	marshaled = append(marshaled, '\n')

	// 資格情報を含みうるので所有者だけが読める権限にする。
	if err := os.WriteFile(path, marshaled, 0o600); err != nil {
		return fmt.Errorf("error at write config file %q: %w", path, err)
	}
	return nil
}

// CheckUnknownKeys は設定ファイルに未知のキーが無いかを調べる。
//
// encoding/json は知らないキーを黙って捨てるので、綴りを間違えた設定が
// 「書いたのに効かない」形で通り抜けてしまう。それを防ぐために明示的に検査する。
// "_" で始まるキーはコメントとして許可する。
func CheckUnknownKeys(raw []byte) error {
	problems := checkUnknownKeys(raw, reflect.TypeOf(Config{}), "")
	return errors.Join(problems...)
}

func checkUnknownKeys(raw []byte, targetType reflect.Type, path string) []error {
	for targetType.Kind() == reflect.Pointer {
		targetType = targetType.Elem()
	}

	switch targetType.Kind() {
	case reflect.Struct:
		return checkUnknownKeysOfStruct(raw, targetType, path)
	case reflect.Slice, reflect.Array:
		var elements []json.RawMessage
		if err := json.Unmarshal(raw, &elements); err != nil {
			// 型違いは Unmarshal 側が報告するので、ここでは黙って通す。
			return nil
		}
		var problems []error
		for i, element := range elements {
			problems = append(problems, checkUnknownKeys(element, targetType.Elem(), fmt.Sprintf("%s[%d]", path, i))...)
		}
		return problems
	default:
		return nil
	}
}

func checkUnknownKeysOfStruct(raw []byte, targetType reflect.Type, path string) []error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil
	}

	known := jsonFieldsOf(targetType)

	var problems []error
	for key, value := range fields {
		if strings.HasPrefix(key, commentKeyPrefix) {
			continue
		}
		fieldType, ok := known[key]
		if !ok {
			problems = append(problems, fmt.Errorf("設定に知らないキーがあります: %s (綴りを確認してください。説明を書きたい場合はキー名を %q で始めてください)", joinPath(path, key), commentKeyPrefix))
			continue
		}
		problems = append(problems, checkUnknownKeys(value, fieldType, joinPath(path, key))...)
	}
	return problems
}

func jsonFieldsOf(targetType reflect.Type) map[string]reflect.Type {
	known := make(map[string]reflect.Type, targetType.NumField())
	for i := range targetType.NumField() {
		field := targetType.Field(i)
		tag := field.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = field.Name
		}
		known[name] = field.Type
	}
	return known
}

func joinPath(path string, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}
