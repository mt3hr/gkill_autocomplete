package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultIsValid(t *testing.T) {
	// 既定値がそのまま通らないと、設定ファイル無しで起動できない。
	if err := Validate(Default()); err != nil {
		t.Fatalf("既定の設定が検証に通らない: %v", err)
	}
}

func TestConfigHasNoCredentialFields(t *testing.T) {
	// 設定に資格情報を書く場所が**そもそも無い**こと。
	//
	// 認証は gkill の設定ディレクトリを直接見て行うので、
	// パスワードもそのハッシュも持つ必要がない。項目が復活すると、
	// 秘密がファイルに置かれる経路もいっしょに戻ってくる。
	marshaled, err := json.Marshal(Default())
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}

	for _, forbidden := range []string{"password", "credential", "session"} {
		if strings.Contains(strings.ToLower(string(marshaled)), forbidden) {
			t.Errorf("設定に %q を含むキーがある: %s", forbidden, marshaled)
		}
	}
}

func TestDefaultHasNoPersonalNames(t *testing.T) {
	// 既定値に実在のタグ名・リポジトリ名を焼き込まないこと。
	// 利用者ごとの値は init が稼働中の gkill を解析して作る。
	defaults := Default()
	if len(defaults.Scope.RepPrefixes) != 0 {
		t.Errorf("既定値に rep_prefixes が入っている: %v", defaults.Scope.RepPrefixes)
	}
	if len(defaults.Exclude.TagPatterns) != 0 {
		t.Errorf("既定値に tag_patterns が入っている: %v", defaults.Exclude.TagPatterns)
	}
	if len(defaults.Rules) != 0 {
		t.Errorf("既定値に rules が入っている: %v", defaults.Rules)
	}
}

func TestIsLoopbackURL(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		want     bool
		wantErr  bool
	}{
		{name: "IPv4ループバック", endpoint: "http://127.0.0.1:11434/v1/chat/completions", want: true},
		{name: "IPv4ループバック別アドレス", endpoint: "http://127.0.0.2:11434/v1/chat/completions", want: true},
		{name: "IPv6ループバック", endpoint: "http://[::1]:11434/v1/chat/completions", want: true},
		{name: "localhost", endpoint: "http://localhost:11434/v1/chat/completions", want: true},
		{name: "LOCALHOST大文字", endpoint: "http://LOCALHOST:11434/v1/chat/completions", want: true},
		{name: "外部ホスト名", endpoint: "https://api.example.com/v1/chat/completions", want: false},
		{name: "LAN内のIP", endpoint: "http://192.168.1.10:11434/v1/chat/completions", want: false},
		{name: "ホスト名なし", endpoint: "/v1/chat/completions", wantErr: true},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := IsLoopbackURL(testCase.endpoint)
			if testCase.wantErr {
				if err == nil {
					t.Fatalf("エラーを期待したが nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("想定外のエラー: %v", err)
			}
			if got != testCase.want {
				t.Errorf("IsLoopbackURL(%q) = %v, want %v", testCase.endpoint, got, testCase.want)
			}
		})
	}
}

func TestValidateRejectsRemoteLLMEndpoint(t *testing.T) {
	// このアプリの最優先の制約。記録の本文と写真を外部へ渡さない。
	target := Default()
	target.LLM.Endpoint = "https://api.example.com/v1/chat/completions"

	err := Validate(target)
	if err == nil {
		t.Fatal("外部のLLMエンドポイントが検証を通ってしまった")
	}
	if !strings.Contains(err.Error(), "llm.endpoint") {
		t.Errorf("どの設定が問題かがエラーに出ていない: %v", err)
	}
}

func TestValidateAllowsRemoteLLMEndpointWhenExplicitlyAllowed(t *testing.T) {
	target := Default()
	target.LLM.Endpoint = "https://api.example.com/v1/chat/completions"
	target.LLM.AllowRemote = true

	if err := Validate(target); err != nil {
		t.Fatalf("allow_remote を明示しても拒否された: %v", err)
	}
}

func TestValidateAllowsNonLoopbackListen(t *testing.T) {
	// 画面は gkill のアカウントによるログインで守られ、通信は
	// gkill と同じ証明書で暗号化される。外へ開くこと自体は拒まない。
	cases := []string{
		"0.0.0.0:9797",
		"192.168.1.10:9797",
		":9797",
	}

	for _, listen := range cases {
		t.Run(listen, func(t *testing.T) {
			target := Default()
			target.Server.Listen = listen
			if err := Validate(target); err != nil {
				t.Fatalf("server.listen = %q が拒否された: %v", listen, err)
			}
		})
	}
}

func TestValidateRejectsMalformedListen(t *testing.T) {
	// 書式そのものが壊れている場合は止める。
	// 起動してから「なぜか繋がらない」となるより早く気づける。
	target := Default()
	target.Server.Listen = "ポートがない"

	if err := Validate(target); err == nil {
		t.Fatal("壊れた server.listen が検証を通ってしまった")
	}
}

func TestValidateAllowsEmptyBaseURL(t *testing.T) {
	// 空なら gkill のサーバ設定から組み立てる。
	// 手で書かせると、gkill 側の設定を変えたときに食い違って繋がらなくなる。
	target := Default()
	target.Gkill.BaseURL = ""

	if err := Validate(target); err != nil {
		t.Fatalf("gkill.base_url が空だと拒否された: %v", err)
	}
}

func TestValidateAcceptsLoopbackListen(t *testing.T) {
	cases := []string{
		"127.0.0.1:9797",
		"[::1]:9797",
		"localhost:9797",
	}

	for _, listen := range cases {
		t.Run(listen, func(t *testing.T) {
			target := Default()
			target.Server.Listen = listen
			if err := Validate(target); err != nil {
				t.Fatalf("server.listen = %q が拒否された: %v", listen, err)
			}
		})
	}
}

func TestParseThumbSize(t *testing.T) {
	cases := []struct {
		input      string
		wantWidth  int
		wantHeight int
		wantOK     bool
	}{
		{input: "400x400", wantWidth: 400, wantHeight: 400, wantOK: true},
		{input: "1024x1024", wantWidth: 1024, wantHeight: 1024, wantOK: true},
		{input: "1x1", wantWidth: 1, wantHeight: 1, wantOK: true},
		// gkill 側の上限を超えると原本(全画素)が返るので弾く。
		{input: "1025x1024", wantOK: false},
		{input: "1024x1025", wantOK: false},
		{input: "0x400", wantOK: false},
		{input: "400", wantOK: false},
		{input: "400X400", wantOK: false},
		{input: "", wantOK: false},
		{input: "12345x100", wantOK: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.input, func(t *testing.T) {
			width, height, ok := ParseThumbSize(testCase.input)
			if ok != testCase.wantOK {
				t.Fatalf("ParseThumbSize(%q) ok = %v, want %v", testCase.input, ok, testCase.wantOK)
			}
			if !ok {
				return
			}
			if width != testCase.wantWidth || height != testCase.wantHeight {
				t.Errorf("ParseThumbSize(%q) = %d,%d want %d,%d", testCase.input, width, height, testCase.wantWidth, testCase.wantHeight)
			}
		})
	}
}

func TestCheckUnknownKeysAllowsCommentKeys(t *testing.T) {
	// 設定ファイル自身に説明を書けること。
	raw := []byte(`{
		"_comment": ["説明", "複数行でもよい"],
		"server": { "_comment": "ここにも書ける", "listen": "127.0.0.1:9797" },
		"rules": [ { "_comment": "配列の中でも書ける", "when": { "tag": "タグA" }, "never_suggest": true } ]
	}`)

	if err := CheckUnknownKeys(raw); err != nil {
		t.Fatalf("コメントキーが拒否された: %v", err)
	}
}

func TestCheckUnknownKeysRejectsTypo(t *testing.T) {
	// 綴り間違いが「書いたのに効かない」形で通り抜けないこと。
	raw := []byte(`{ "scoring": { "threshhold": 0.8 } }`)

	err := CheckUnknownKeys(raw)
	if err == nil {
		t.Fatal("綴り間違いのキーが通ってしまった")
	}
	if !strings.Contains(err.Error(), "scoring.threshhold") {
		t.Errorf("どのキーが問題かがエラーに出ていない: %v", err)
	}
}

func TestParseKeepsDefaultsForAbsentKeys(t *testing.T) {
	raw := []byte(`{ "scoring": { "threshold": 0.9 } }`)

	parsed, err := Parse(raw)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if parsed.Scoring.Threshold != 0.9 {
		t.Errorf("threshold = %v, want 0.9", parsed.Scoring.Threshold)
	}
	// 同じ節の他の項目が既定のまま残ること。
	if parsed.Scoring.NeighborWindowMinutes != Default().Scoring.NeighborWindowMinutes {
		t.Errorf("書いていない項目が既定から変わった: %v", parsed.Scoring.NeighborWindowMinutes)
	}
	// 別の節が既定のまま残ること。
	if parsed.Candidates.MinExamples != Default().Candidates.MinExamples {
		t.Errorf("別の節が既定から変わった: %v", parsed.Candidates.MinExamples)
	}
}

func TestParseAllowsExplicitFalse(t *testing.T) {
	// 既定が true の項目を false にできること。
	// 既定値入りの構造体へ上書きする方式なので、ここを取り違えると
	// 「false を書いても効かない」ことになる。
	raw := []byte(`{ "exclude": { "already_tagged": false } }`)

	parsed, err := Parse(raw)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if parsed.Exclude.AlreadyTagged {
		t.Error("already_tagged に false を書いたのに true のまま")
	}
}

func TestParseRejectsRuleWithoutCondition(t *testing.T) {
	// 条件の無いルールは全記録に効いてしまう。
	raw := []byte(`{ "rules": [ { "suggest": ["タグA"] } ] }`)

	if _, err := Parse(raw); err == nil {
		t.Fatal("条件の無いルールが通ってしまった")
	}
}

func TestValidateRejectsLearnDaysShorterThanCandidateDays(t *testing.T) {
	target := Default()
	target.Scope.CandidateDays = 90
	target.Scope.LearnDays = 30

	if err := Validate(target); err == nil {
		t.Fatal("学習範囲が候補範囲より狭い設定が通ってしまった")
	}
}

func TestValidateCollectsAllProblems(t *testing.T) {
	// 1つ直すたびに起動し直さずに済むよう、問題はまとめて返す。
	target := Default()
	target.Server.Listen = "ポートがない"
	target.LLM.Endpoint = "https://api.example.com/v1"
	target.LLM.ThumbSize = "9999x9999"

	err := Validate(target)
	if err == nil {
		t.Fatal("検証を通ってしまった")
	}
	message := err.Error()
	for _, want := range []string{"server.listen", "llm.endpoint", "llm.thumb_size"} {
		if !strings.Contains(message, want) {
			t.Errorf("%q がエラーに含まれていない: %v", want, message)
		}
	}
}

func TestSaveWritesNoSecrets(t *testing.T) {
	// 設定ファイルに秘密が入らないこと。
	//
	// いまは資格情報を持つ項目が無いので当たり前に見えるが、
	// うっかり項目を足したときにここで気づけるようにしておく。
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, Default()); err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	for _, forbidden := range []string{"password", "credential", "session"} {
		if strings.Contains(strings.ToLower(string(written)), forbidden) {
			t.Errorf("設定ファイルに %q を含むキーが書かれている: %s", forbidden, written)
		}
	}
}

func TestSaveRefusesToOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, Default()); err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if err := Save(path, Default()); err == nil {
		t.Fatal("既存のファイルを上書きしてしまった")
	}
}

func TestMarshalIndentRedactedRoundTrips(t *testing.T) {
	// 出力の入口はここ1箇所に保つ。秘密を持つ項目が将来増えたとき、
	// 「出力する側を全部直す」ことにならないようにするため。
	target := Default()

	marshaled, err := target.MarshalIndentRedacted()
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}

	// 出したものをそのまま読み戻せること(設定ファイルの雛形として使えるように)。
	parsed, err := Parse(marshaled)
	if err != nil {
		t.Fatalf("出力した設定を読み戻せない: %v", err)
	}
	if parsed.Server.Listen != target.Server.Listen {
		t.Errorf("往復で値が変わった: %q != %q", parsed.Server.Listen, target.Server.Listen)
	}
}
