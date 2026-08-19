// Package websrv は確認画面を配信し、承認と却下を受け付ける。
//
// 画面には未確認の提案、つまり利用者の記録の中身がそのまま並ぶ。
// そのため次の2つを守る。
//
//	認証 … gkill のアカウントで守る(照合は internal/gkillauth)。
//	暗号 … gkill 本体と同じ証明書で TLS を張る。
//
// **見せてよいのはログインした本人の記録だけ。** 記録に触れる口はすべて
// requireAuth を通し、保存先への問い合わせは必ず利用者IDで絞る。
package websrv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/app"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/config"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/gkillauth"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/gkillclient"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/store"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/suggest"
)

// Server は確認画面のサーバ。
type Server struct {
	// apps は利用者IDごとの解析本体。起動時に --user へ渡した人ぶんだけある。
	//
	// ログインできるのは gkill の全アカウントだが、提案を持つのはここにいる人だけ。
	apps     map[string]*app.App
	frontend fs.FS
	logger   *slog.Logger
	auth     *authenticator

	// serveTLS は TLS で待ち受けているか。クッキーの Secure 属性に使う。
	serveTLS bool

	// baseCtx は解析の寿命の元になる文脈。
	//
	// **リクエストの文脈を使ってはいけない。** 解析は数十分かかるので、
	// タブを閉じる・再読込する・端末が眠るといったことで切れてしまう。
	// Serve が待ち受けの文脈を入れ、終了時にまとめて止まるようにする。
	baseCtx context.Context

	// mu は analyses と imageIndex を守る。
	mu sync.Mutex
	// analyses は利用者ごとの解析の進み具合。二重起動もこれで防ぐ。
	analyses map[string]*analysis
	// imageIndex は利用者IDごとの「記録ID → 写真の場所」の対応。
	//
	// 一覧を作るたびに更新する。これがあるおかげで、画像の中継口に
	// リポジトリ名やファイル名を外から渡させずに済む。
	//
	// **利用者ごとに分けているのは、他人の一覧に載った写真を
	// 記録IDだけで引けてしまわないようにするため。**
	imageIndex map[string]map[string]imageLocation
}

type imageLocation struct {
	RepName  string
	FileName string
}

// New はサーバを作る。
//
// apps は解析対象の利用者ぶん。verifier はログインの照合に使う。
// frontend は埋め込んだ画面のファイル群。
func New(apps []*app.App, verifier *gkillauth.Verifier, frontend fs.FS, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}

	byUser := make(map[string]*app.App, len(apps))
	for _, application := range apps {
		byUser[application.UserID()] = application
	}

	return &Server{
		apps:       byUser,
		frontend:   frontend,
		logger:     logger,
		baseCtx:    context.Background(),
		analyses:   map[string]*analysis{},
		imageIndex: map[string]map[string]imageLocation{},
		auth:       newAuthenticator(verifier, logger),
	}
}

// appFor はその利用者の解析本体を返す。用意されていなければ nil。
func (s *Server) appFor(userID string) *app.App {
	return s.apps[userID]
}

// storeOf は保存先を返す。どの App も同じ保存先を共有している。
func (s *Server) storeOf() *store.Store {
	for _, application := range s.apps {
		return application.Store
	}
	return nil
}

// Handler はルーティング済みのハンドラを返す。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// ログインまわりだけは認証を要らない。
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /api/session", s.handleSession)

	// 記録の中身に触れる口はすべて認証の後ろに置く。
	mux.HandleFunc("POST /api/suggestions", s.requireAuth(s.handleSuggestions))
	mux.HandleFunc("POST /api/decide", s.requireAuth(s.handleDecide))
	mux.HandleFunc("POST /api/analyze", s.requireAuth(s.handleAnalyze))
	mux.HandleFunc("GET /api/analyze/status", s.requireAuth(s.handleAnalyzeStatus))
	mux.HandleFunc("GET /thumb", s.requireAuth(s.handleThumb))

	if s.frontend != nil {
		mux.Handle("/", http.FileServerFS(s.frontend))
	}

	return mux
}

// ServeOptions は待ち受けの設定。
type ServeOptions struct {
	// Listen は bind するアドレス。
	Listen string

	// TLS は gkill 本体から読み取った証明書の設定。
	//
	// EnableTLS が偽なら平文で開く。gkill が TLS を切っている構成で
	// こちらだけ TLS にすると、利用者から見て食い違うため。
	TLS gkillauth.ServerSettings
}

// Serve は待ち受けを始める。
//
// 証明書は gkill 本体が使っているものをそのまま使う。専用のものを作らないのは、
// 利用者に証明書をもう1枚信頼させないため。
func (s *Server) Serve(ctx context.Context, options ServeOptions) error {
	s.serveTLS = options.TLS.EnableTLS

	// 解析はこの文脈にぶら下げる。リクエストが切れても止まらず、
	// Ctrl+C で終了するときだけまとめて止まる。
	s.mu.Lock()
	s.baseCtx = ctx
	s.mu.Unlock()

	server := &http.Server{
		Addr:              options.Listen,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	scheme := "http"
	if s.serveTLS {
		scheme = "https"
	}
	s.logger.Info("確認画面を開きました",
		slog.String("url", scheme+"://"+options.Listen+"/"))
	s.warnIfExposed(options.Listen)

	var err error
	if s.serveTLS {
		if certErr := options.TLS.EnsureTLSFilesExist(); certErr != nil {
			return certErr
		}
		err = server.ListenAndServeTLS(options.TLS.CertFile, options.TLS.KeyFile)
	} else {
		err = server.ListenAndServe()
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("error at serve: %w", err)
	}
	return nil
}

// warnIfExposed はループバック以外に開いていることを毎回知らせる。
//
// 設定を一度書けば以後は黙って開き続けるので、開いていること自体を
// 忘れないよう起動のたびに出す。
func (s *Server) warnIfExposed(listen string) {
	loopback, err := config.IsLoopbackListenAddr(listen)
	if err != nil || loopback {
		return
	}

	if !s.serveTLS {
		s.logger.Warn(
			"確認画面をループバック以外に、暗号化せずに開いています。"+
				"同じ網にいる相手には、やり取りする資格情報や記録の中身が見えます。"+
				"gkill 側で TLS を有効にしてください",
			slog.String("bind", listen))
		return
	}

	// gkill が作る証明書は localhost 向けなので、別端末からは
	// 「証明書の名前が一致しない」と言われる。開くたびに理由を出しておく。
	s.logger.Warn(
		"確認画面をループバック以外に開いています。gkill と同じ証明書を使っていますが、"+
			"その証明書は localhost 向けに作られているため、"+
			"別の端末のブラウザでは名前が一致しないという警告が出ます",
		slog.String("bind", listen))
}

// writeStatusError は状態コードつきで失敗を伝える。
func (s *Server) writeStatusError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// suggestionView は画面に出す提案1件。
type suggestionView struct {
	ID         string  `json:"id"`
	Tag        string  `json:"tag"`
	Confidence float64 `json:"confidence"`
	Tier       string  `json:"tier"`
	Reason     string  `json:"reason"`
}

// recordView は画面に出す記録1件。
type recordView struct {
	TargetID    string    `json:"target_id"`
	DataType    string    `json:"data_type"`
	RelatedTime time.Time `json:"related_time"`
	IsImage     bool      `json:"is_image"`
	ThumbURL    string    `json:"thumb_url"`

	// FileName はファイルの記録(idf)の名前。
	//
	// **画像でない idf は写真も本文も持たない。** 出さないと、日時と種別しか
	// 載っていない空の札になり、何を判定させられているのか分からない。
	// gkill は画像以外のサムネイルを作らないので、写真の代わりに名前を出す。
	FileName string `json:"file_name"`

	Text        string           `json:"text"`
	ExistingTag []string         `json:"existing_tags"`
	Suggestions []suggestionView `json:"suggestions"`
}

type suggestionsResponse struct {
	Records []recordView `json:"records"`
	Pending int          `json:"pending"`

	// Skipped は「中身を取りに行ったのに gkill から返ってこなかった」記録の数。
	//
	// **0件の理由を画面が言い分けられるようにするためにある。**
	// これが無いと、確認待ちが何千件あっても一覧が空なら
	// 「確認待ちの提案はありません」としか出せず、
	// 取得が壊れているのか本当に片付いているのかが区別できない。
	Skipped int `json:"skipped"`
}

const (
	// maxRecordsPerResponse は1回の一覧で中身まで取り出す記録の数。
	//
	// 画面は1件ずつ捌くので、確認待ち全部の中身を毎回引く必要はない。
	// 全部引くと応答が大きくなるうえ、gkill 側の検索が数十秒かかる。
	// 捌いて空になったら画面が読み直す(use-tag-suggestion-page.ts)。
	maxRecordsPerResponse = 200

	// fetchRecordsChunkSize は1回のリクエストで gkill へ渡す記録IDの数。
	//
	// ★IDの一覧は必ず分割して渡すこと。★
	//
	// gkill の Mi 検索は5射影の UNION で、5本それぞれに ID の一覧を丸ごと
	// 展開する(dao/reps/mi_repository_sqlite3_impl.go)。バインド変数は 5N+5 に
	// なり、SQLite の上限 32766 を **N=6553 で超える**(実測: 6552 は成功、
	// 6553 で破綻)。しかも超えたときに返るのはエラーではなく **空の結果** で、
	// gkill 側のハンドラが「err はあるが GkillError は無い」場合に
	// レスポンスへ何も積まないまま return するため、HTTP 200 + errors:null に
	// 見える。2026-08-18 に確認待ちが上限を超える件数まで溜まった状態で実際に踏み、
	// 「確認待ちは残っているのに一覧だけが空」という形で表面化した。
	fetchRecordsChunkSize = 500
)

func (s *Server) handleSuggestions(w http.ResponseWriter, r *http.Request, userID string) {
	ctx := r.Context()

	application := s.appFor(userID)
	if application == nil {
		// ログインはできるが解析対象ではない利用者。
		// 他人の提案を見せるわけにはいかないので、空を返す。
		s.writeJSON(w, suggestionsResponse{Records: []recordView{}, Pending: 0})
		return
	}

	pending, err := application.Store.ListPending(ctx, userID)
	if err != nil {
		s.writeError(w, err)
		return
	}

	// 記録ごとにまとめる。画面は1件ずつ順に捌くため。
	order := []string{}
	byTarget := map[string][]store.Suggestion{}
	for _, suggestion := range pending {
		if _, ok := byTarget[suggestion.TargetID]; !ok {
			order = append(order, suggestion.TargetID)
		}
		byTarget[suggestion.TargetID] = append(byTarget[suggestion.TargetID], suggestion)
	}

	records, examined, err := s.fetchRecords(ctx, application, order, maxRecordsPerResponse)
	if err != nil {
		s.writeError(w, err)
		return
	}

	response := suggestionsResponse{Records: []recordView{}, Pending: len(pending)}
	// 中身を取りに行った範囲だけを見る。まだ取りに行っていない残りは
	// 「消えた記録」ではないので Skipped に数えてはいけない。
	for _, targetID := range order[:examined] {
		record, ok := records[targetID]
		if !ok {
			// gkill 側から消えた記録。画面には出さないが、数だけは伝える。
			// 黙って捨てると、取得そのものが壊れたときに
			// 「確認待ちは片付いた」と見分けが付かなくなる。
			response.Skipped++
			continue
		}

		view := recordView{
			TargetID:    targetID,
			DataType:    record.DataType,
			RelatedTime: record.RelatedTime,
			IsImage:     record.IsImage,
			FileName:    record.FileName,
			Text:        record.Text,
			ExistingTag: record.Tags,
			Suggestions: []suggestionView{},
		}
		// 写真の中継は画像のときだけ。gkill の ?thumb= は画像以外に
		// サムネイルを作らず原本をそのまま返すので、動画や書類を
		// 画像として引きに行かせない。
		if record.IsImage && record.FileName != "" {
			view.ThumbURL = "/thumb?target=" + targetID
		}
		for _, suggestion := range byTarget[targetID] {
			view.Suggestions = append(view.Suggestions, suggestionView{
				ID:         suggestion.ID,
				Tag:        suggestion.Tag,
				Confidence: suggestion.Confidence,
				Tier:       suggestion.Tier,
				Reason:     suggestion.Reason,
			})
		}
		response.Records = append(response.Records, view)
	}

	s.writeJSON(w, response)
}

// fetchRecords は記録の中身を gkill から取り直す。
//
// 提案の保存先には記録の中身を置いていない。画面に出すものは
// そのつど取り直すことで、gkill 側で消したり直したりした結果が
// そのまま反映される。
//
// targetIDs は新しい順。want 件ぶん取れた時点で打ち切り、
// 実際に見に行った件数を第2の戻り値で返す。
// **IDは fetchRecordsChunkSize ずつに割って渡す。** 理由は同定数のコメントを見ること。
// 取りに行った範囲で返ってこなかったIDは「gkill 側から消えた記録」で、
// 呼び出し元が数える。
func (s *Server) fetchRecords(ctx context.Context, application *app.App, targetIDs []string, want int) (map[string]suggest.Record, int, error) {
	records := map[string]suggest.Record{}
	index := map[string]imageLocation{}

	examined := 0
	for examined < len(targetIDs) && len(records) < want {
		end := min(examined+fetchRecordsChunkSize, len(targetIDs))

		kyous, err := application.Client.FetchKyous(ctx, &gkillclient.FindQuery{
			IDs:            targetIDs[examined:end],
			OnlyLatestData: true,
		}, gkillclient.FetchOptions{IncludeID: true})
		if err != nil {
			return nil, 0, err
		}

		for _, kyou := range kyous {
			record := suggest.FromKyou(kyou)
			records[record.ID] = record
			if record.IsImage && record.FileName != "" {
				index[record.ID] = imageLocation{RepName: record.RepName, FileName: record.FileName}
			}
		}
		examined = end
	}

	// 索引はこの利用者のぶんだけ差し替える。他の利用者の索引は残す。
	s.mu.Lock()
	s.imageIndex[application.UserID()] = index
	s.mu.Unlock()

	return records, examined, nil
}

type decideRequest struct {
	TargetID string `json:"target_id"`
	// ApproveTags は承認するタグ。gkill へ書き込む。
	ApproveTags []string `json:"approve_tags"`
	// NoTagNeeded はこの記録にタグは要らないという判定。
	NoTagNeeded bool `json:"no_tag_needed"`
}

type decideResponse struct {
	Approved int `json:"approved"`
	Rejected int `json:"rejected"`
	Pending  int `json:"pending"`
}

// handleDecide は承認と却下を受け付ける。
//
// 承認したタグだけを gkill へ書き込み、同じ記録の残りの提案は却下として畳む。
// 何も承認せずに確定した場合は「タグは要らない」として記録する。
func (s *Server) handleDecide(w http.ResponseWriter, r *http.Request, userID string) {
	ctx := r.Context()

	application := s.appFor(userID)
	if application == nil {
		s.writeStatusError(w, http.StatusForbidden,
			"このアカウントは解析の対象に含まれていません")
		return
	}

	request := decideRequest{}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		s.writeError(w, fmt.Errorf("リクエストを解釈できません: %w", err))
		return
	}
	if request.TargetID == "" {
		s.writeError(w, errors.New("target_id がありません"))
		return
	}

	// 自分の未判定の提案しか取れない。他人の提案IDを送りつけられても
	// この一覧に無いので、下のループで一致せず何も起きない。
	pending, err := application.Store.ListPending(ctx, userID)
	if err != nil {
		s.writeError(w, err)
		return
	}

	approve := map[string]bool{}
	for _, tagName := range request.ApproveTags {
		approve[tagName] = true
	}

	response := decideResponse{}
	now := time.Now()

	for _, suggestion := range pending {
		if suggestion.TargetID != request.TargetID {
			continue
		}

		if !approve[suggestion.Tag] {
			// 承認しなかったものは却下として畳む。
			// そうしないと同じ記録が何度も出てくる。
			if err := application.Store.Decide(ctx, userID, suggestion.ID, store.DecisionRejected, now); err != nil {
				s.writeError(w, err)
				return
			}
			response.Rejected++
			continue
		}

		if err := s.approveTag(ctx, application, suggestion, now); err != nil {
			s.writeError(w, err)
			return
		}
		response.Approved++
	}

	if request.NoTagNeeded && response.Approved == 0 {
		if err := application.Store.MarkNoTagNeeded(ctx, userID, request.TargetID, now); err != nil {
			s.writeError(w, err)
			return
		}
	}

	remaining, err := application.Store.CountPending(ctx, userID)
	if err != nil {
		s.writeError(w, err)
		return
	}
	response.Pending = remaining

	s.writeJSON(w, response)
}

// approveTag はタグを gkill へ書き込み、承認として記録する。
func (s *Server) approveTag(ctx context.Context, application *app.App, suggestion store.Suggestion, now time.Time) error {
	deviceName, err := os.Hostname()
	if err != nil {
		deviceName = "unknown"
	}

	userID := application.UserID()

	tag := gkillclient.NewTag(
		suggestion.TagID,
		suggestion.TargetID,
		suggestion.Tag,
		// 記録そのものの時刻に合わせる。省くと時系列の表示から外れてしまう。
		suggestion.RelatedTime,
		userID,
		deviceName,
		now,
	)

	alreadyExist, err := application.Client.AddTag(ctx, tag)
	if err != nil {
		return err
	}
	if alreadyExist {
		// 過去に付けて手で消したか、二度承認したか。どちらも
		// 「何もしない」が正しい。蘇らせない。
		s.logger.Info("同じIDのタグが既にあるため書き込みませんでした")
	}

	return application.Store.Decide(ctx, userID, suggestion.ID, store.DecisionApproved, now)
}

// analysis は利用者1人ぶんの解析の状態。
//
// **解析はリクエストより長生きする。** 写真の判定は1件で数分かかり、
// 数十件あれば1時間を超える。画面はこれを覗きに来るだけにして、
// タブを閉じても解析が続くようにしてある。
type analysis struct {
	running   bool
	done      int
	total     int
	startedAt time.Time

	// report は直近の解析の結果。まだ一度も終わっていなければ nil。
	report *app.AnalyzeReport
	// failure は直近の解析が落ちた理由。落ちていなければ空。
	failure string
}

// analyzeStatusResponse は解析の進み具合。
//
// **失敗の理由を "error" というキーで返してはいけない。** 画面側の共通処理が
// その名前を「要求そのものが失敗した」と解釈して例外にするため、
// 解析の失敗とリクエストの失敗が混ざる。
type analyzeStatusResponse struct {
	Running bool `json:"running"`
	Done    int  `json:"done"`
	Total   int  `json:"total"`

	// Report は直近の解析の結果。走っている間や、一度も走っていなければ null。
	Report *app.AnalyzeReport `json:"report"`
	// Failure は直近の解析が落ちた理由。
	Failure string `json:"failure,omitempty"`

	Pending int `json:"pending"`
}

func (s *Server) handleAnalyze(w http.ResponseWriter, r *http.Request, userID string) {
	application := s.appFor(userID)
	if application == nil {
		s.writeStatusError(w, http.StatusForbidden,
			"このアカウントは解析の対象に含まれていません。"+
				"起動時に --user で指定してください")
		return
	}

	// 二重起動は利用者ごとに防ぐ。別の人の解析までは止めない。
	// すでに走っている場合も失敗にはしない。画面は状態を見に来るだけでよい。
	s.mu.Lock()
	current, ok := s.analyses[userID]
	if !ok {
		current = &analysis{}
		s.analyses[userID] = current
	}
	alreadyRunning := current.running
	if !alreadyRunning {
		current.running = true
		current.done = 0
		current.total = 0
		current.startedAt = time.Now()
		current.report = nil
		current.failure = ""
	}
	analyzeCtx := s.baseCtx
	s.mu.Unlock()

	if !alreadyRunning {
		go s.runAnalysis(analyzeCtx, application, userID)
	}

	s.writeJSON(w, s.analyzeStatus(r.Context(), application, userID))
}

// runAnalysis は解析を最後まで走らせる。リクエストとは別の寿命で動く。
func (s *Server) runAnalysis(ctx context.Context, application *app.App, userID string) {
	report, err := application.AnalyzeWithProgress(ctx, func(progress app.Progress) {
		s.mu.Lock()
		if current, ok := s.analyses[userID]; ok {
			current.done = progress.Done
			current.total = progress.Total
		}
		s.mu.Unlock()
	})

	s.mu.Lock()
	defer s.mu.Unlock()

	current, ok := s.analyses[userID]
	if !ok {
		return
	}
	current.running = false
	if err != nil {
		// エラー本文には記録の中身が混ざりうるので、ログには要約しか出さない。
		s.logger.Warn("解析が途中で終わりました")
		current.failure = err.Error()
		return
	}
	current.report = &report
}

func (s *Server) handleAnalyzeStatus(w http.ResponseWriter, r *http.Request, userID string) {
	application := s.appFor(userID)
	if application == nil {
		s.writeJSON(w, analyzeStatusResponse{})
		return
	}
	s.writeJSON(w, s.analyzeStatus(r.Context(), application, userID))
}

// analyzeStatus はいまの進み具合を組み立てる。
func (s *Server) analyzeStatus(ctx context.Context, application *app.App, userID string) analyzeStatusResponse {
	s.mu.Lock()
	response := analyzeStatusResponse{}
	if current, ok := s.analyses[userID]; ok {
		response.Running = current.running
		response.Done = current.done
		response.Total = current.total
		response.Report = current.report
		response.Failure = current.failure
	}
	s.mu.Unlock()

	pending, err := application.Store.CountPending(ctx, userID)
	if err != nil {
		s.logger.Warn("確認待ちの件数を数えられませんでした")
		return response
	}
	response.Pending = pending
	return response
}

// handleThumb は写真を中継する。
//
// 受け取るのは記録のIDだけ。リポジトリ名やファイル名を外から渡させないので、
// 一覧に出ていない場所のファイルを読ませることはできない。
// 取得したものはディスクに残さない。
//
// 索引は利用者ごとなので、**他人の一覧に載っている写真は記録IDを知っていても引けない**。
func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request, userID string) {
	targetID := r.URL.Query().Get("target")
	if targetID == "" {
		http.Error(w, "target がありません", http.StatusBadRequest)
		return
	}

	application := s.appFor(userID)
	if application == nil {
		http.Error(w, "その記録の写真は一覧にありません", http.StatusNotFound)
		return
	}

	s.mu.Lock()
	location, ok := s.imageIndex[userID][targetID]
	s.mu.Unlock()

	if !ok {
		http.Error(w, "その記録の写真は一覧にありません", http.StatusNotFound)
		return
	}

	image, err := application.Client.FetchThumb(r.Context(), location.RepName, location.FileName, application.Config.LLM.ThumbSize)
	if err != nil {
		s.logger.Warn("写真を取得できませんでした", slog.String("error", err.Error()))
		http.Error(w, "写真を取得できませんでした", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", image.ContentType)
	// 記録の中身なので、ブラウザ以外に残さない。
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(image.Bytes)
}

func (s *Server) writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		s.logger.Warn("応答を書けませんでした", slog.String("error", err.Error()))
	}
}

// writeError は失敗を伝える。
//
// エラー本文には記録の中身が混ざりうるので、画面には出すが
// ログには要約しか出さない。
func (s *Server) writeError(w http.ResponseWriter, err error) {
	s.logger.Warn("要求を処理できませんでした")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
