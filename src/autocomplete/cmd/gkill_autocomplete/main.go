// gkill_autocomplete は gkill の記録に付けるべきタグを提案する。
//
// 提案するだけで自分では書き込まない。タグを付けるのは、確認画面で
// 人が承認したときだけ。
//
// # 認証について
//
// **このコマンドは資格情報を受け取らない。** 渡すのは利用者IDだけで、
// gkill を叩くためのセッションは gkill の設定ディレクトリへ直接書いて発行する
// (gkill 自身の auto_tag / update_cache と同じ手口)。
// 信頼の根拠は「gkill と同じ端末で設定ディレクトリに書けること」。
//
// 確認画面のログインは gkill のアカウントで行う。全アカウントが対象で、
// 見えるのは自分の提案だけ。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/app"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/classify"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/config"
	gkillembed "github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/embed"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/gkillauth"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/gkillclient"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/llm"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/store"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/suggest"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/websrv"
)

var (
	configPathFlag  string
	logLevelFlag    string
	printConfigFlag bool
	onceFlag        bool
	userFlags       []string
)

func main() {
	if err := newRootCommand().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	rootCommand := &cobra.Command{
		Use:   "gkill_autocomplete --user <利用者ID>",
		Short: "gkill の記録に付けるべきタグを提案する",
		Long: "gkill の記録に付けるべきタグを提案します。\n" +
			"提案するだけで書き込みは行いません。タグを付けるのは確認画面で承認したときだけです。\n" +
			"判定に使う LLM はローカルのものに限られます。\n" +
			"\n" +
			"パスワードは要りません。利用者IDだけを渡してください。\n" +
			"gkill と同じ端末で動かすことが、そのまま権限の根拠になります。",
		Example: "  # 確認画面を開く\n" +
			"  gkill_autocomplete --user sample_user\n" +
			"\n" +
			"  # 解析だけを1回行う\n" +
			"  gkill_autocomplete --user sample_user --once\n" +
			"\n" +
			"  # 複数の利用者をまとめて扱う\n" +
			"  gkill_autocomplete --user sample_user --user sample_user_all",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(command *cobra.Command, _ []string) error {
			if printConfigFlag {
				return runPrintConfig(command)
			}
			if onceFlag {
				return runAnalyze(command.Context())
			}
			return runServe(command.Context())
		},
	}

	rootCommand.PersistentFlags().StringVar(&configPathFlag, "config", "", "設定ファイルの場所 (既定: $GKILL_AUTOCOMPLETE_HOME/config.json)")
	rootCommand.PersistentFlags().StringVar(&logLevelFlag, "log", "info", "ログの詳しさ: debug / info / warn / error")
	rootCommand.PersistentFlags().StringArrayVar(&userFlags, "user", nil, "対象にする gkill の利用者ID (繰り返し指定できます)")
	rootCommand.Flags().BoolVar(&printConfigFlag, "print-config", false, "既定の設定を出力して終わる")
	rootCommand.Flags().BoolVar(&onceFlag, "once", false, "解析を1回だけ行って終わる (確認画面は開かない)")

	rootCommand.AddCommand(newInitCommand())
	rootCommand.AddCommand(newBenchmarkCommand())
	rootCommand.AddCommand(newResetCommand())

	return rootCommand
}

func newResetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "reset",
		Short: "提案を捨てて、次の解析でやり直せるようにする",
		Long: "保存済みの提案と「判定済みの印」を消します。\n" +
			"閾値や対象範囲を変えたあと、その設定で付け直したいときに使います。\n" +
			"\n" +
			"**承認・却下・タグ不要の判定は消しません。** これらは作り直せない情報で、\n" +
			"消すと却下したはずの提案が次の解析で全部復活します。\n" +
			"\n" +
			"消えるのは --user で指定した利用者のぶんだけです。",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(command *cobra.Command, _ []string) error {
			return runReset(command.Context())
		},
	}
}

func runReset(ctx context.Context) error {
	opened, err := openApps(ctx, false)
	if err != nil {
		return err
	}
	defer opened.Close(ctx)

	for _, application := range opened.Apps {
		userID := application.UserID()

		before, err := application.Store.CountPending(ctx, userID)
		if err != nil {
			return err
		}
		if err := application.Store.ClearSuggestions(ctx, userID); err != nil {
			return err
		}

		decided, err := application.Store.DecidedTargetIDs(ctx, userID)
		if err != nil {
			return err
		}

		fmt.Printf("[%s] 提案を %d件 捨てました。\n", userID, before)
		fmt.Printf("[%s] 人の判定 %d件 は残してあります(却下したものが復活しないように)。\n", userID, len(decided))
	}

	fmt.Println("`gkill_autocomplete --user <利用者ID> --once` で付け直せます。")
	return nil
}

func newBenchmarkCommand() *cobra.Command {
	fromFlag := ""
	toFlag := ""

	benchmarkCommand := &cobra.Command{
		Use:   "benchmark",
		Short: "正解の分かっている期間で提案の精度を測る",
		Long: "既にタグを付け終えた過去の期間を使って、提案がどれくらい当たるかを測ります。\n" +
			"学習には計測期間より前の記録だけを使うので、自分の答えを見て当てることはありません。\n" +
			"保存先にも gkill にも一切書き込みません。",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(command *cobra.Command, _ []string) error {
			from, err := time.Parse(time.DateOnly, fromFlag)
			if err != nil {
				return fmt.Errorf("--from は YYYY-MM-DD の形で指定してください: %w", err)
			}
			to, err := time.Parse(time.DateOnly, toFlag)
			if err != nil {
				return fmt.Errorf("--to は YYYY-MM-DD の形で指定してください: %w", err)
			}
			return runBenchmark(command.Context(), app.BenchmarkOptions{From: from, To: to})
		},
	}

	benchmarkCommand.Flags().StringVar(&fromFlag, "from", "", "計測期間の開始 (YYYY-MM-DD)")
	benchmarkCommand.Flags().StringVar(&toFlag, "to", "", "計測期間の終了 (YYYY-MM-DD、この日は含まない)")
	_ = benchmarkCommand.MarkFlagRequired("from")
	_ = benchmarkCommand.MarkFlagRequired("to")

	return benchmarkCommand
}

func runBenchmark(ctx context.Context, options app.BenchmarkOptions) error {
	opened, err := openApps(ctx, true)
	if err != nil {
		return err
	}
	defer opened.Close(ctx)

	application, err := opened.Single("benchmark")
	if err != nil {
		return err
	}

	fmt.Printf("[%s] %s 〜 %s の記録で精度を測ります(書き込みは行いません)\n",
		application.UserID(),
		options.From.Format(time.DateOnly), options.To.Format(time.DateOnly))

	report, err := application.Benchmark(ctx, options)
	if err != nil {
		return err
	}

	fmt.Print(report.Summary())
	return nil
}

func newInitCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "稼働中の gkill を解析して設定ファイルを作る",
		Long: "稼働中の gkill を解析し、あなたの使い方に合わせた設定ファイルを作ります。\n" +
			"既にファイルがある場合は上書きしません。",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(command *cobra.Command, _ []string) error {
			return runInit(command.Context())
		},
	}
}

// setupLogger はログの出力先と詳しさを決める。
//
// 記録の本文はどの詳しさでも出さない。出すのは件数と所要時間だけ。
func setupLogger() {
	level := slog.LevelInfo
	switch logLevelFlag {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
}

// resolveConfigPath は設定ファイルの場所を決める。
func resolveConfigPath() (string, error) {
	if configPathFlag != "" {
		return configPathFlag, nil
	}
	return config.ConfigPath()
}

func runPrintConfig(command *cobra.Command) error {
	marshaled, err := config.Default().MarshalIndentRedacted()
	if err != nil {
		return err
	}
	fmt.Fprintln(command.OutOrStdout(), string(marshaled))
	return nil
}

// appSet は起動時に組み立てた一式。
type appSet struct {
	Config config.Config

	// Apps は --user で指定された利用者ぶんの解析本体。
	Apps []*app.App

	// Verifier は確認画面のログインを照合する。
	Verifier *gkillauth.Verifier

	// Server は gkill 本体のサーバ設定(TLS 証明書の場所と宛先)。
	Server gkillauth.ServerSettings

	store     *store.Store
	providers []*gkillauth.SessionProvider
}

// Single はちょうど1人ぶんの App を返す。init や benchmark で使う。
func (s *appSet) Single(commandName string) (*app.App, error) {
	if len(s.Apps) != 1 {
		return nil, fmt.Errorf(
			"%s は利用者を1人だけ指定してください (いまは %d 人)。\n"+
				"  例: gkill_autocomplete %s --user sample_user",
			commandName, len(s.Apps), commandName)
	}
	return s.Apps[0], nil
}

// Close は発行したセッションを片付け、保存先を閉じる。
func (s *appSet) Close(ctx context.Context) {
	// セッションは gkill の DB に残るので、必ず消してから終わる。
	for _, provider := range s.providers {
		provider.Close(ctx)
	}
	if s.store != nil {
		if err := s.store.Close(); err != nil {
			slog.Warn("保存先を閉じられませんでした", slog.String("error", err.Error()))
		}
	}
}

// resolveUserIDs は対象の利用者IDを決める。
func resolveUserIDs() ([]string, error) {
	seen := map[string]bool{}
	userIDs := make([]string, 0, len(userFlags))
	for _, raw := range userFlags {
		userID := strings.TrimSpace(raw)
		if userID == "" {
			continue
		}
		if seen[userID] {
			continue
		}
		seen[userID] = true
		userIDs = append(userIDs, userID)
	}

	if len(userIDs) == 0 {
		return nil, errors.New(
			"対象の利用者を指定してください。\n" +
				"  例: gkill_autocomplete --user sample_user\n" +
				"\n" +
				"  パスワードは要りません。gkill と同じ端末で動かすこと自体が権限の根拠になります。")
	}
	return userIDs, nil
}

// openApps は設定を読み、gkill と保存先へ繋いだ App を利用者ぶん作る。
//
// needsClassifier が偽のときは判定器を作らない。init のように LLM を使わない
// 場面で「モデルが設定されていません」と言われても、何をすればよいか分からないため。
func openApps(ctx context.Context, needsClassifier bool) (*appSet, error) {
	setupLogger()

	userIDs, err := resolveUserIDs()
	if err != nil {
		return nil, err
	}

	configPath, err := resolveConfigPath()
	if err != nil {
		return nil, err
	}

	loaded, err := config.LoadOrDefault(configPath)
	if err != nil {
		return nil, err
	}

	gkillHome := gkillauth.GkillHome(loaded.Gkill.Home)
	configDir := gkillauth.ConfigDir(gkillHome)

	// **アカウントDBを DAO で開く前に、必ずスキーマの版を確かめる。**
	// 旧版のまま開くと gkill 側が自動移行を走らせ、
	// 全アカウントのパスワードが無効化されてしまう。
	if err := gkillauth.EnsureAccountSchemaIsCurrent(ctx, gkillauth.AccountDBPath(configDir)); err != nil {
		return nil, err
	}

	serverSettings, err := gkillauth.LoadServerSettings(ctx, configDir, slog.Default())
	if err != nil {
		return nil, err
	}

	// 宛先は設定に書いてあればそれ、無ければ gkill のサーバ設定から。
	baseURL := strings.TrimSpace(loaded.Gkill.BaseURL)
	if baseURL == "" {
		baseURL = serverSettings.BaseURL
	}

	home, err := config.Home()
	if err != nil {
		return nil, err
	}

	openedStore, err := store.Open(ctx, filepath.Join(home, config.StoreFileName))
	if err != nil {
		return nil, err
	}

	opened := &appSet{
		Config:   loaded,
		Verifier: gkillauth.NewVerifier(gkillauth.AccountDBPath(configDir), slog.Default()),
		Server:   serverSettings,
		store:    openedStore,
	}

	llmClient := llm.New(loaded.LLM.Endpoint, loaded.LLM.TextModel, loaded.LLM.VisionModel, loaded.LLM.Timeout())

	for _, userID := range userIDs {
		provider := gkillauth.NewSessionProvider(configDir, userID, serverSettings.Device, slog.Default())

		// ここでセッションを1つ作ってみる。アカウントが無い・無効といった
		// 間違いを、解析を始める前に見つけるため。
		if _, err := provider.SessionID(ctx); err != nil {
			opened.Close(ctx)
			return nil, err
		}
		opened.providers = append(opened.providers, provider)

		client, err := gkillclient.New(loaded.Gkill, baseURL, provider)
		if err != nil {
			opened.Close(ctx)
			return nil, err
		}

		application := &app.App{
			Config: loaded,
			Client: client,
			Store:  openedStore,
			// init がモデルを調べるのに使う。判定そのものには使わない。
			Models: llmClient,
			Logger: slog.Default().With(slog.String("user", userID)),
		}
		if needsClassifier {
			application.Classifier = buildClassifier(loaded, llmClient, client)
		}
		opened.Apps = append(opened.Apps, application)
	}

	return opened, nil
}

// buildClassifier は LLM を使う判定器を組み立てる。
//
// モデルが1つも設定されていない場合は nil を返す。そのときは
// 逐語一致と近傍の記録による判定だけで動く(LLM は無くても使える)。
func buildClassifier(loaded config.Config, llmClient *llm.Client, client *gkillclient.Client) suggest.Classifier {
	if loaded.LLM.TextModel == "" && loaded.LLM.VisionModel == "" {
		slog.Warn("LLM のモデルが設定されていないため、本文の一致と近くの記録だけで判定します。" +
			"写真の判定を使うには、設定の llm.vision_model にモデル名を書いてください " +
			"(`gkill_autocomplete init` が使えるモデルを調べて書き込みます)")
		return nil
	}
	return classify.New(llmClient, client, loaded.LLM.ThumbSize, loaded.Candidates.MaxFewShotImages)
}

func runAnalyze(ctx context.Context) error {
	opened, err := openApps(ctx, true)
	if err != nil {
		return err
	}
	defer opened.Close(ctx)

	for _, application := range opened.Apps {
		userID := application.UserID()

		report, err := application.Analyze(ctx)
		if err != nil {
			return fmt.Errorf("[%s] %w", userID, err)
		}

		fmt.Printf("[%s] 学習した記録: %d件\n", userID, report.LearnedRecords)
		fmt.Printf("[%s] 判定した記録: %d件\n", userID, report.CandidateRecords)
		fmt.Printf("[%s]   提案あり: %d件\n", userID, report.SuggestedRecords)
		fmt.Printf("[%s]   提案なし: %d件 (タグを付けないことも正常な結果です)\n", userID, report.NoSuggestionRecords)
		fmt.Printf("[%s] 保存した提案: %d件\n", userID, report.StoredSuggestions)
		if report.SkippedByVerdict > 0 {
			fmt.Printf("[%s] 過去の判定により見送った提案: %d件\n", userID, report.SkippedByVerdict)
		}

		pending, err := application.Store.CountPending(ctx, userID)
		if err != nil {
			return err
		}
		fmt.Printf("[%s] 確認待ちの提案: %d件\n", userID, pending)
	}
	return nil
}

// runServe は確認画面を開く。
func runServe(ctx context.Context) error {
	opened, err := openApps(ctx, true)
	if err != nil {
		return err
	}
	defer opened.Close(ctx)

	if !gkillembed.IsBuilt() {
		return errors.New("確認画面が組み込まれていません。`npm run install_app` でビルドし直してください。\n" +
			"解析だけなら `gkill_autocomplete --user <利用者ID> --once` で行えます")
	}
	frontend, err := gkillembed.Frontend()
	if err != nil {
		return fmt.Errorf("error at open embedded frontend: %w", err)
	}

	for _, application := range opened.Apps {
		userID := application.UserID()
		pending, err := application.Store.CountPending(ctx, userID)
		if err != nil {
			return err
		}
		fmt.Printf("[%s] 確認待ちの提案: %d件\n", userID, pending)
	}

	scheme := "http"
	if opened.Server.EnableTLS {
		scheme = "https"
	}
	fmt.Printf("ブラウザで %s://%s/ を開いてください\n", scheme, opened.Config.Server.Listen)
	fmt.Println("ログインは gkill の利用者IDとパスワードです。")

	// Ctrl+C で止められるようにする。
	serveCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	server := websrv.New(opened.Apps, opened.Verifier, frontend, slog.Default())
	return server.Serve(serveCtx, websrv.ServeOptions{
		Listen: opened.Config.Server.Listen,
		TLS:    opened.Server,
	})
}

func runInit(ctx context.Context) error {
	setupLogger()

	configPath, err := resolveConfigPath()
	if err != nil {
		return err
	}

	// 先に置き場を確かめる。解析を終えてから「既にある」と言われては徒労になる。
	if _, err := os.Stat(configPath); err == nil {
		return fmt.Errorf("設定ファイルが既にあります: %s\n"+
			"作り直したい場合は、いまのファイルを退避してから実行してください", configPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("error at stat config file %q: %w", configPath, err)
	}

	// init は LLM を使わない。判定器は作らない。
	opened, err := openApps(ctx, false)
	if err != nil {
		return err
	}
	defer opened.Close(ctx)

	application, err := opened.Single("init")
	if err != nil {
		return err
	}

	fmt.Println("稼働中の gkill を読んで、使い方を調べています...")

	built, report, err := application.BuildInitialConfig(ctx)
	if err != nil {
		return err
	}

	fmt.Print(report.Summary())

	if err := config.Save(configPath, built); err != nil {
		return err
	}

	fmt.Printf("\n設定ファイルを作りました: %s\n", configPath)
	fmt.Println("中身を確認して、必要なら scope や rules を調整してください。")
	fmt.Println()
	fmt.Printf("準備ができたら `gkill_autocomplete --user %s --once` で解析できます。\n",
		application.UserID())
	return nil
}
