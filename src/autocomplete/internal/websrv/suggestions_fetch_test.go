package websrv

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
)

// TestSuggestionsSplitsTargetIDsForGkill は、記録の中身を取りに行くときに
// 記録IDを分割して渡していることを固定する。
//
// 分割せずに確認待ちの全件を1回のクエリで渡すと、gkill の Mi 検索が
// 5射影の UNION でIDの一覧を5回展開するため、バインド変数が SQLite の上限
// (32766)を N=6553 で超える。**そのとき gkill が返すのはエラーではなく空の結果**
// なので、確認待ちが何千件あっても一覧だけが黙って空になる。
// 2026-08-18 に確認待ちが上限を超える件数まで溜まった状態で実際に踏んだ回帰。
func TestSuggestionsSplitsTargetIDsForGkill(t *testing.T) {
	fake := newFakeGkill(t, nil)

	mutex := sync.Mutex{}
	maxIDsPerRequest := 0
	requestCount := 0
	fake.onGetKyous = func(requestedIDs []string) []map[string]any {
		mutex.Lock()
		if len(requestedIDs) > maxIDsPerRequest {
			maxIDsPerRequest = len(requestedIDs)
		}
		requestCount++
		mutex.Unlock()

		kyous := []map[string]any{}
		for _, id := range requestedIDs {
			kyous = append(kyous, kyouJSON(id, map[string]any{"kind": "kmemo", "content": "本文"}))
		}
		return kyous
	}

	server, openedStore := newTestServer(t, fake)

	targetCount := fetchRecordsChunkSize + 100
	for i := range targetCount {
		putSuggestion(t, openedStore, fmt.Sprintf("target-%05d", i), "タグA")
	}

	recorder := doPost(t, server, "/api/suggestions", map[string]any{})
	if recorder.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", recorder.Code, recorder.Body.String())
	}

	response := suggestionsResponse{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("応答を解釈できない: %v", err)
	}

	if maxIDsPerRequest > fetchRecordsChunkSize {
		t.Errorf("1回のリクエストで渡した記録ID = %d件, want %d件以下 (分割されていない)",
			maxIDsPerRequest, fetchRecordsChunkSize)
	}
	if requestCount == 0 {
		t.Fatal("gkill へ1回も問い合わせていない")
	}
	// 確認待ちが残っているのに一覧が空、が起きていないこと。
	if len(response.Records) == 0 {
		t.Errorf("確認待ちが %d件あるのに一覧が空", response.Pending)
	}
	if response.Pending != targetCount {
		t.Errorf("確認待ち = %d件, want %d件", response.Pending, targetCount)
	}
	if response.Skipped != 0 {
		t.Errorf("取り出せなかった記録 = %d件, want 0件", response.Skipped)
	}
}

// TestSuggestionsStopsFetchingOnceEnoughRecords は、画面に出すぶんが揃ったら
// 残りの中身を取りに行かないことを固定する。
//
// 画面は1件ずつ捌くので、確認待ち全部の中身を毎回引く必要はない。
// 全部引くと gkill 側の検索が確認待ちの件数ぶん重くなる。
func TestSuggestionsStopsFetchingOnceEnoughRecords(t *testing.T) {
	fake := newFakeGkill(t, nil)

	mutex := sync.Mutex{}
	totalRequestedIDs := 0
	fake.onGetKyous = func(requestedIDs []string) []map[string]any {
		mutex.Lock()
		totalRequestedIDs += len(requestedIDs)
		mutex.Unlock()

		kyous := []map[string]any{}
		for _, id := range requestedIDs {
			kyous = append(kyous, kyouJSON(id, map[string]any{"kind": "kmemo", "content": "本文"}))
		}
		return kyous
	}

	server, openedStore := newTestServer(t, fake)

	targetCount := fetchRecordsChunkSize * 3
	for i := range targetCount {
		putSuggestion(t, openedStore, fmt.Sprintf("target-%05d", i), "タグA")
	}

	recorder := doPost(t, server, "/api/suggestions", map[string]any{})
	if recorder.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", recorder.Code, recorder.Body.String())
	}

	if totalRequestedIDs >= targetCount {
		t.Errorf("取りに行った記録ID = %d件, want %d件未満 (確認待ち全件を引いている)",
			totalRequestedIDs, targetCount)
	}
}
