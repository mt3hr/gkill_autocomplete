package classify

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"testing"
	"time"

	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/gkillclient"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/llm"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/suggest"
)

// 実物の LLM を相手にする検査。
//
// 既定では走らない。LLM は同じ入力でも必ずしも同じ答えを返さないので、
// ふだんのテストに混ぜると理由なく落ちるようになるため。
//
// 走らせ方:
//
//	GKILL_AUTOCOMPLETE_LLM_INTEGRATION=1 go test ./internal/classify/ -run Integration -v
//
// 環境変数で接続先とモデルを指定できる。
//
//	GKILL_AUTOCOMPLETE_LLM_ENDPOINT (既定 http://127.0.0.1:11434/v1/chat/completions)
//	GKILL_AUTOCOMPLETE_LLM_TEXT_MODEL
//	GKILL_AUTOCOMPLETE_LLM_VISION_MODEL
func skipUnlessIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("GKILL_AUTOCOMPLETE_LLM_INTEGRATION") != "1" {
		t.Skip("実物の LLM を使う検査。GKILL_AUTOCOMPLETE_LLM_INTEGRATION=1 で有効になる")
	}
}

func integrationEndpoint() string {
	if value := os.Getenv("GKILL_AUTOCOMPLETE_LLM_ENDPOINT"); value != "" {
		return value
	}
	return "http://127.0.0.1:11434/v1/chat/completions"
}

// solidColorJPEG は一色で塗りつぶした JPEG を作る。
//
// 利用者の写真を使わずに、画像を渡す経路が通ることだけを確かめるためのもの。
func solidColorJPEG(t *testing.T, fill color.RGBA) []byte {
	t.Helper()

	canvas := image.NewRGBA(image.Rect(0, 0, 96, 96))
	for x := range 96 {
		for y := range 96 {
			canvas.Set(x, y, fill)
		}
	}

	buffer := &bytes.Buffer{}
	if err := jpeg.Encode(buffer, canvas, nil); err != nil {
		t.Fatalf("画像を作れない: %v", err)
	}
	return buffer.Bytes()
}

// fixedImages は決まった画像を返す。
type fixedImages struct {
	bytes []byte
}

func (f *fixedImages) FetchThumb(_ context.Context, _ string, _ string, _ string) (gkillclient.Image, error) {
	return gkillclient.Image{Bytes: f.bytes, ContentType: "image/jpeg"}, nil
}

func TestIntegrationTextModelReturnsWellFormedJudgements(t *testing.T) {
	skipUnlessIntegration(t)

	model := os.Getenv("GKILL_AUTOCOMPLETE_LLM_TEXT_MODEL")
	if model == "" {
		t.Skip("GKILL_AUTOCOMPLETE_LLM_TEXT_MODEL が指定されていない")
	}

	classifier := New(llm.New(integrationEndpoint(), model, "", 3*time.Minute), &fixedImages{}, "400x400", 2)

	record := suggest.Record{ID: "x", DataType: "kmemo", Text: "赤い果物を食べた"}
	candidates := []suggest.Candidate{
		{Tag: "くだもの", TextExamples: []string{"りんごを食べた"}},
		{Tag: "のりもの", TextExamples: []string{"電車に乗った"}},
	}

	judgements, err := classifier.Classify(context.Background(), record, candidates)
	if err != nil {
		t.Fatalf("実物の LLM との往復に失敗: %v", err)
	}

	// 中身の正しさは問わない。応答が解釈でき、候補の外へ出ないことを見る。
	allowed := map[string]bool{"くだもの": true, "のりもの": true}
	for _, judgement := range judgements {
		if !allowed[judgement.Tag] {
			t.Errorf("候補に無いタグが返った: %q", judgement.Tag)
		}
		if judgement.Confidence < 0 || judgement.Confidence > 1 {
			t.Errorf("確信度が範囲外: %v", judgement.Confidence)
		}
	}
	t.Logf("判定 %d件", len(judgements))
}

func TestIntegrationVisionModelAcceptsImages(t *testing.T) {
	skipUnlessIntegration(t)

	model := os.Getenv("GKILL_AUTOCOMPLETE_LLM_VISION_MODEL")
	if model == "" {
		t.Skip("GKILL_AUTOCOMPLETE_LLM_VISION_MODEL が指定されていない")
	}

	// 利用者の写真は使わない。画像を渡す経路が通ることだけを確かめる。
	//
	// 見本写真を1枚に絞ってあるのは、文脈長の既定(4096)でも収まるようにするため。
	// 写真は1枚で千数百トークンになるので、見本を増やすとすぐ溢れる。
	images := &fixedImages{bytes: solidColorJPEG(t, color.RGBA{R: 220, G: 30, B: 30, A: 255})}
	classifier := New(llm.New(integrationEndpoint(), "", model, 5*time.Minute), images, "400x400", 1)

	record := suggest.Record{
		ID: "x", DataType: "idf", IsImage: true,
		RepName: "SampleRep_DeviceA_20200101", FileName: "target.jpg",
	}
	candidates := []suggest.Candidate{
		{Tag: "あかいろ", ImageExamples: []suggest.ImageExample{{RepName: "SampleRep_DeviceA_20200101", FileName: "example.jpg"}}},
		{Tag: "あおいろ"},
	}

	judgements, err := classifier.Classify(context.Background(), record, candidates)
	if err != nil {
		t.Fatalf("実物の視覚モデルとの往復に失敗: %v", err)
	}

	allowed := map[string]bool{"あかいろ": true, "あおいろ": true}
	for _, judgement := range judgements {
		if !allowed[judgement.Tag] {
			t.Errorf("候補に無いタグが返った: %q", judgement.Tag)
		}
	}
	t.Logf("判定 %d件", len(judgements))
	for _, judgement := range judgements {
		t.Logf("  %s: yes=%v confidence=%.2f", judgement.Tag, judgement.Yes, judgement.Confidence)
	}
}
