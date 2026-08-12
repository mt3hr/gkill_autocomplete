package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLooksLikeVisionModel(t *testing.T) {
	// 名前でしか判断できないので、よくある付き方を押さえる。
	cases := map[string]bool{
		"qwen2.5vl:7b":        true,
		"llama3.2-vision:11b": true,
		"llava:13b":           true,
		"minicpm-v:8b":        true,
		"some-vl-model":       true,
		"MODEL-VL:LATEST":     true,
		"swallow-jp:8b-q4km":  false,
		"qwen3:8b":            false,
		"nemoaurora-chat:12b": false,
		"novel-otter:7b":      false,
		// "vl" が語の途中に紛れている場合は拾わない。
		"vladimir-model:7b": false,
		"":                  false,
	}

	for name, want := range cases {
		if got := LooksLikeVisionModel(name); got != want {
			t.Errorf("LooksLikeVisionModel(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestCompleteConstrainsTheAnswer(t *testing.T) {
	// 実運用で踏んだもの。上限も形の指定も渡していなかったため、
	// モデルが同じ判定を繰り返し続けて文脈長を使い切り、
	// 閉じ括弧の無い JSON を返して ErrBadResponse になった
	// (2026-08-12: 1件に8分08秒かけて約4,460トークン)。
	//
	// 指示文でお願いするだけでは足りないので、送る側で縛れていることを固定する。
	received := chatRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("リクエストを読めない: %v", err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}]}`))
	}))
	defer server.Close()

	client := New(server.URL, "text-model", "", 10*time.Second)
	if _, err := client.Complete(context.Background(), "text-model", []Message{
		{Role: RoleUser, Parts: []Part{TextPart("本文")}},
	}); err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}

	if received.MaxTokens != maxAnswerTokens {
		t.Errorf("max_tokens = %d, want %d (無制限だと終わらない応答が起きる)", received.MaxTokens, maxAnswerTokens)
	}
	if received.ResponseFormat == nil {
		t.Fatal("response_format が付いていない (JSON を文法で縛れていない)")
	}
	if received.ResponseFormat.Type != responseFormatJSONObject {
		t.Errorf("response_format.type = %q, want %q", received.ResponseFormat.Type, responseFormatJSONObject)
	}
	// 判定の揺れを抑えるための 0 が消えていないこと。
	if received.Temperature != 0 {
		t.Errorf("temperature = %v, want 0", received.Temperature)
	}
}

func TestCompleteExplainsContextSizeError(t *testing.T) {
	// 実運用で踏んだもの。写真の見本を数枚添えるだけで既定の 4096 を超え、
	// 素のエラー文だけでは何を直せばよいか分からなかった。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"request (7486 tokens) exceeds the available context size (4096 tokens), try increasing it","type":"exceed_context_size_error","n_ctx":4096}}`))
	}))
	defer server.Close()

	client := New(server.URL, "text-model", "", 10*time.Second)

	_, err := client.Complete(context.Background(), "text-model", []Message{
		{Role: RoleUser, Parts: []Part{TextPart("本文")}},
	})
	if err == nil {
		t.Fatal("エラーを期待したが nil")
	}

	message := err.Error()
	// 直し方は2つある。どちらも案内に出ること。
	for _, want := range []string{"OLLAMA_CONTEXT_LENGTH", "max_few_shot_images", "文脈長"} {
		if !strings.Contains(message, want) {
			t.Errorf("%q が案内に含まれていない: %v", want, message)
		}
	}
}

func TestCompleteReportsOtherErrorsPlainly(t *testing.T) {
	// 文脈長と無関係な失敗に、見当違いの案内を出さないこと。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"something else"}}`))
	}))
	defer server.Close()

	client := New(server.URL, "text-model", "", 10*time.Second)

	_, err := client.Complete(context.Background(), "text-model", []Message{
		{Role: RoleUser, Parts: []Part{TextPart("本文")}},
	})
	if err == nil {
		t.Fatal("エラーを期待したが nil")
	}
	if strings.Contains(err.Error(), "OLLAMA_CONTEXT_LENGTH") {
		t.Errorf("関係のない案内が出ている: %v", err)
	}
}

func TestListModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("パス = %q, want /v1/models", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "zebra:7b"},
				{"id": "alpha:8b"},
				{"id": ""},
			},
		})
	}))
	defer server.Close()

	client := New(server.URL+"/v1/chat/completions", "", "", 10*time.Second)

	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	// 名前が空のものは落とし、並びは安定させる。
	if len(models) != 2 || models[0] != "alpha:8b" || models[1] != "zebra:7b" {
		t.Errorf("モデル = %v", models)
	}
}

func TestListModelsReportsUnreachableLLM(t *testing.T) {
	// 接続できないポートへ向ける。
	client := New("http://127.0.0.1:1/v1/chat/completions", "", "", 2*time.Second)

	_, err := client.ListModels(context.Background())
	if err == nil {
		t.Fatal("エラーを期待したが nil")
	}
	if !strings.Contains(err.Error(), "起動しているか") {
		t.Errorf("原因の手がかりが無い: %v", err)
	}
}

func TestModelsEndpointDerivation(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1:11434/v1/chat/completions": "http://127.0.0.1:11434/v1/models",
		"http://127.0.0.1:8080/v1/chat/completions":  "http://127.0.0.1:8080/v1/models",
		// 想定と違う形でも、それらしい場所を組み立てる。
		"http://127.0.0.1:11434/v1/": "http://127.0.0.1:11434/v1/models",
	}

	for endpoint, want := range cases {
		client := New(endpoint, "", "", time.Second)
		if got := client.modelsEndpoint(); got != want {
			t.Errorf("modelsEndpoint(%q) = %q, want %q", endpoint, got, want)
		}
	}
}
