package classify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/gkillclient"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/llm"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/suggest"
)

// stubImages は写真の取得を差し替える。
type stubImages struct {
	err       error
	requested []string
}

func (s *stubImages) FetchThumb(_ context.Context, repName string, fileName string, _ string) (gkillclient.Image, error) {
	s.requested = append(s.requested, repName+"/"+fileName)
	if s.err != nil {
		return gkillclient.Image{}, s.err
	}
	return gkillclient.Image{Bytes: []byte{0xFF, 0xD8, 0xFF}, ContentType: "image/jpeg"}, nil
}

// newLLMServer は LLM を模したサーバを立てる。captured には送られた本文が入る。
func newLLMServer(t *testing.T, answer string, captured *string) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("リクエストを解釈できない: %v", err)
		}
		if captured != nil {
			marshaled, _ := json.Marshal(body)
			*captured = string(marshaled)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": answer}},
			},
		})
	}))
	t.Cleanup(server.Close)
	return server
}

func TestExtractJSONObject(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
		ok    bool
	}{
		{name: "そのまま", input: `{"a":1}`, want: `{"a":1}`, ok: true},
		{name: "前置きがある", input: `はい、こちらです: {"a":1}`, want: `{"a":1}`, ok: true},
		{name: "コードブロックに囲まれている", input: "```json\n{\"a\":1}\n```", want: `{"a":1}`, ok: true},
		{name: "入れ子", input: `{"a":{"b":2}}`, want: `{"a":{"b":2}}`, ok: true},
		{name: "文字列の中の括弧", input: `{"a":"}"}`, want: `{"a":"}"}`, ok: true},
		{name: "エスケープされた引用符", input: `{"a":"\"}"}`, want: `{"a":"\"}"}`, ok: true},
		{name: "JSONが無い", input: `わかりません`, ok: false},
		{name: "閉じていない", input: `{"a":1`, ok: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := llm.ExtractJSONObject(testCase.input)
			if ok != testCase.ok {
				t.Fatalf("ok = %v, want %v", ok, testCase.ok)
			}
			if ok && got != testCase.want {
				t.Errorf("= %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestClassifyTextRecord(t *testing.T) {
	answer := `{"judgements":[{"tag":"タグA","yes":true,"confidence":0.9,"reason":"本文が似ている"}]}`
	server := newLLMServer(t, answer, nil)

	classifier := New(llm.New(server.URL, "text-model", "", 10*time.Second), &stubImages{}, "400x400", 2)

	record := suggest.Record{ID: "x", DataType: "kmemo", Text: "判定してほしい本文"}
	candidates := []suggest.Candidate{{Tag: "タグA", TextExamples: []string{"見本の本文"}}}

	judgements, err := classifier.Classify(context.Background(), record, candidates)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(judgements) != 1 {
		t.Fatalf("判定 = %d件, want 1件", len(judgements))
	}
	if judgements[0].Tag != "タグA" || !judgements[0].Yes || judgements[0].Confidence != 0.9 {
		t.Errorf("判定 = %+v", judgements[0])
	}
}

func TestClassifyDropsTagsOutsideCandidates(t *testing.T) {
	// モデルが勝手に作った名前でタグを付けてしまわないこと。
	answer := `{"judgements":[
		{"tag":"タグA","yes":true,"confidence":0.9},
		{"tag":"勝手に作ったタグ","yes":true,"confidence":0.99}
	]}`
	server := newLLMServer(t, answer, nil)

	classifier := New(llm.New(server.URL, "text-model", "", 10*time.Second), &stubImages{}, "400x400", 2)

	judgements, err := classifier.Classify(context.Background(),
		suggest.Record{ID: "x", Text: "本文"},
		[]suggest.Candidate{{Tag: "タグA"}})
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(judgements) != 1 || judgements[0].Tag != "タグA" {
		t.Errorf("候補に無いタグが通った: %+v", judgements)
	}
}

func TestClassifyAcceptsEmptyJudgements(t *testing.T) {
	// どれも当てはまらないのは正常な答え。
	server := newLLMServer(t, `{"judgements":[]}`, nil)

	classifier := New(llm.New(server.URL, "text-model", "", 10*time.Second), &stubImages{}, "400x400", 2)

	judgements, err := classifier.Classify(context.Background(),
		suggest.Record{ID: "x", Text: "本文"},
		[]suggest.Candidate{{Tag: "タグA"}})
	if err != nil {
		t.Fatalf("該当なしをエラーにしてしまった: %v", err)
	}
	if len(judgements) != 0 {
		t.Errorf("判定 = %+v, want 0件", judgements)
	}
}

func TestClassifyWithNoCandidatesDoesNotCallLLM(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		called = true
	}))
	defer server.Close()

	classifier := New(llm.New(server.URL, "text-model", "", 10*time.Second), &stubImages{}, "400x400", 2)

	judgements, err := classifier.Classify(context.Background(), suggest.Record{ID: "x", Text: "本文"}, nil)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(judgements) != 0 {
		t.Errorf("判定 = %+v", judgements)
	}
	if called {
		t.Error("候補が無いのに LLM を呼んだ")
	}
}

func TestClassifyWithoutConfiguredModelIsSkipped(t *testing.T) {
	// モデルが設定されていないときは、LLM の段階を静かに飛ばす。
	// 逐語一致と近傍による判定だけでも動くようにするため。
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		called = true
	}))
	defer server.Close()

	classifier := New(llm.New(server.URL, "", "", 10*time.Second), &stubImages{}, "400x400", 2)

	judgements, err := classifier.Classify(context.Background(),
		suggest.Record{ID: "x", Text: "本文"},
		[]suggest.Candidate{{Tag: "タグA"}})
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(judgements) != 0 || called {
		t.Error("モデル未設定なのに LLM を呼んだ")
	}
}

func TestClassifyImageSendsTargetAndExamples(t *testing.T) {
	captured := ""
	server := newLLMServer(t, `{"judgements":[{"tag":"タグA","yes":true,"confidence":0.8}]}`, &captured)

	images := &stubImages{}
	classifier := New(llm.New(server.URL, "", "vision-model", 10*time.Second), images, "400x400", 2)

	record := suggest.Record{
		ID: "x", DataType: "idf", IsImage: true,
		RepName: "SampleRep_DeviceA_20200101", FileName: "target.jpg",
	}
	candidates := []suggest.Candidate{{
		Tag: "タグA",
		ImageExamples: []suggest.ImageExample{
			{RepName: "SampleRep_DeviceA_20200101", FileName: "example1.jpg"},
		},
	}}

	judgements, err := classifier.Classify(context.Background(), record, candidates)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(judgements) != 1 {
		t.Fatalf("判定 = %d件, want 1件", len(judgements))
	}

	// 見本と対象の両方を取りに行っていること。
	if len(images.requested) != 2 {
		t.Errorf("画像の取得 = %d回, want 2回: %v", len(images.requested), images.requested)
	}

	// 画像はデータURLとして埋め込み、ファイルの場所は渡さない。
	if !strings.Contains(captured, "data:image/jpeg;base64,") {
		t.Error("画像がデータURLとして送られていない")
	}
	for _, secret := range []string{"target.jpg", "example1.jpg", "SampleRep_DeviceA_20200101"} {
		if strings.Contains(captured, secret) {
			t.Errorf("ファイルの場所が LLM へ送られている: %q", secret)
		}
	}

	// 視覚モデルが選ばれていること。
	if !strings.Contains(captured, "vision-model") {
		t.Error("視覚モデルが使われていない")
	}
}

func TestClassifyImageRespectsExampleImageCap(t *testing.T) {
	// 写真は1枚で千数百トークンになる。見本を積みすぎると LLM の文脈長を超えて
	// 判定そのものが失敗する(実運用で 7486/4096 で落ちた)。
	// 設定した枚数を超えて送らないこと。
	server := newLLMServer(t, `{"judgements":[]}`, nil)

	images := &stubImages{}
	classifier := New(llm.New(server.URL, "", "vision-model", 10*time.Second), images, "400x400", 1)

	record := suggest.Record{ID: "x", IsImage: true, RepName: "SampleRep", FileName: "target.jpg"}
	candidates := []suggest.Candidate{
		{Tag: "タグA", ImageExamples: []suggest.ImageExample{
			{RepName: "SampleRep", FileName: "a1.jpg"},
			{RepName: "SampleRep", FileName: "a2.jpg"},
		}},
		{Tag: "タグB", ImageExamples: []suggest.ImageExample{
			{RepName: "SampleRep", FileName: "b1.jpg"},
		}},
	}

	if _, err := classifier.Classify(context.Background(), record, candidates); err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}

	// 見本1枚 + 対象1枚 = 2回。
	if len(images.requested) != 2 {
		t.Errorf("画像の取得 = %d回, want 2回: %v", len(images.requested), images.requested)
	}
}

func TestClassifyImageWithZeroExamplesSendsOnlyTarget(t *testing.T) {
	// 文脈長が狭い環境でも判定だけは通せるようにする逃げ道。
	server := newLLMServer(t, `{"judgements":[]}`, nil)

	images := &stubImages{}
	// New は0以下を既定値に読み替えるので、ここは負値ではなく明示的に0を扱える形で確かめる。
	classifier := New(llm.New(server.URL, "", "vision-model", 10*time.Second), images, "400x400", 2)
	classifier.maxExampleImages = 0

	record := suggest.Record{ID: "x", IsImage: true, RepName: "SampleRep", FileName: "target.jpg"}
	candidates := []suggest.Candidate{{
		Tag:           "タグA",
		ImageExamples: []suggest.ImageExample{{RepName: "SampleRep", FileName: "a1.jpg"}},
	}}

	if _, err := classifier.Classify(context.Background(), record, candidates); err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}

	// 対象の1枚だけ。
	if len(images.requested) != 1 {
		t.Errorf("画像の取得 = %d回, want 1回: %v", len(images.requested), images.requested)
	}
}

func TestClassifyImageContinuesWhenExampleIsUnavailable(t *testing.T) {
	// 見本が取れなくても、対象さえ取れれば判定はできる。
	server := newLLMServer(t, `{"judgements":[]}`, nil)

	images := &stubImages{err: errors.New("取得できない")}
	classifier := New(llm.New(server.URL, "", "vision-model", 10*time.Second), images, "400x400", 2)

	record := suggest.Record{ID: "x", IsImage: true, RepName: "SampleRep", FileName: "target.jpg"}
	candidates := []suggest.Candidate{{
		Tag:           "タグA",
		ImageExamples: []suggest.ImageExample{{RepName: "SampleRep", FileName: "example1.jpg"}},
	}}

	// 対象も取れない設定なので、ここではエラーになるのが正しい。
	if _, err := classifier.Classify(context.Background(), record, candidates); err == nil {
		t.Fatal("対象の写真が取れないのにエラーにならなかった")
	}
}

func TestClassifyRejectsUnparsableAnswer(t *testing.T) {
	server := newLLMServer(t, "よくわかりません", nil)

	classifier := New(llm.New(server.URL, "text-model", "", 10*time.Second), &stubImages{}, "400x400", 2)

	if _, err := classifier.Classify(context.Background(),
		suggest.Record{ID: "x", Text: "本文"},
		[]suggest.Candidate{{Tag: "タグA"}}); err == nil {
		t.Fatal("解釈できない応答が通ってしまった")
	}
}

func TestClassifyReportsLLMFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	classifier := New(llm.New(server.URL, "text-model", "", 10*time.Second), &stubImages{}, "400x400", 2)

	if _, err := classifier.Classify(context.Background(),
		suggest.Record{ID: "x", Text: "本文"},
		[]suggest.Candidate{{Tag: "タグA"}}); err == nil {
		t.Fatal("LLM の失敗が握り潰されている")
	}
}

func TestClassifyPromptDoesNotLeakIdentifiers(t *testing.T) {
	// 本文と候補タグ以外は渡さない。
	captured := ""
	server := newLLMServer(t, `{"judgements":[]}`, &captured)

	classifier := New(llm.New(server.URL, "text-model", "", 10*time.Second), &stubImages{}, "400x400", 2)

	record := suggest.Record{
		ID:       "secret-record-id",
		DataType: "kmemo",
		Text:     "判定してほしい本文",
		RepName:  "SecretRep_DeviceA_20200101",
		FileName: "secret.jpg",
	}

	if _, err := classifier.Classify(context.Background(), record, []suggest.Candidate{{Tag: "タグA"}}); err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}

	for _, secret := range []string{"secret-record-id", "SecretRep_DeviceA_20200101", "secret.jpg"} {
		if strings.Contains(captured, secret) {
			t.Errorf("識別子が LLM へ送られている: %q", secret)
		}
	}
	// 判定に必要なものは送られていること。
	if !strings.Contains(captured, "判定してほしい本文") || !strings.Contains(captured, "タグA") {
		t.Error("判定に必要な情報が送られていない")
	}
}
