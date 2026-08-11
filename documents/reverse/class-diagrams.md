# クラス図

Go と TypeScript の主要な型と、その関係です。

## 1. パッケージの依存

依存は一方向です。`suggest` は `config` と `gkillclient` しか知らず、
`llm` や `websrv` を知りません。**LLM を差し替えても `suggest` は変わりません。**

```mermaid
graph TD
    cmd[cmd/gkill_autocomplete]
    app[internal/app]
    suggest[internal/suggest]
    classify[internal/classify]
    llm[internal/llm]
    store[internal/store]
    ids[internal/ids]
    config[internal/config]
    gkillclient[internal/gkillclient]
    gkillauth[internal/gkillauth]
    websrv[internal/websrv]
    embedpkg[internal/embed]

    cmd --> app
    cmd --> websrv
    cmd --> gkillauth
    cmd --> llm
    cmd --> classify
    cmd --> store

    app --> suggest
    app --> gkillclient
    app --> store
    app --> config
    app --> ids

    suggest --> config
    suggest --> gkillclient

    classify --> llm
    classify --> gkillclient
    classify --> suggest

    websrv --> app
    websrv --> gkillauth
    websrv --> embedpkg

    gkillclient --> config

    style gkillauth fill:#fdd,stroke:#333
    style suggest fill:#bbf,stroke:#333
```

赤は gkill 本体のコードを import する唯一の場所です。

## 2. 判定エンジン（`internal/suggest`）

```mermaid
classDiagram
    class Record {
        +string ID
        +string DataType
        +time.Time RelatedTime
        +[]string Tags
        +string Title
        +string Text
        +string RepName
        +string RepFamily
        +bool IsImage
        +string FileName
        +ContextKey() string
        +HasAnyTag() bool
    }

    class Knowledge {
        -map~string,TagStat~ tags
        -map~string,int~ contextTotals
        -map~string,[]string~ textIndex
        +Candidates(Record, CandidatesOptions) []Candidate
        +TagsForExactText(string) []string
        +UntaggedRate(contextKey string) float64
        +TagStatOf(string) (TagStat, bool)
        +TagNames() []string
        +ContextKeys() []string
        +ContextTotal(string) int
    }

    class TagStat {
        +string Name
        +int Total
        +map~string,int~ ByContext
        +[24]int ByHour
        +[]string TextExamples
        +[]ImageExample ImageExamples
    }

    class Candidate {
        +string Tag
        +float64 Prior
        +int Examples
    }

    class Engine {
        -Knowledge knowledge
        -ScoringConfig scoring
        -CandidatesConfig candidates
        -[]Rule rules
        -Classifier classifier
        +Suggest(ctx, Record, neighbors) (Result, error)
        -dampenByHabit(Record, []Suggestion) []Suggestion
        -suggestByRules(...) []Suggestion
        -suggestByExactText(...) []Suggestion
        -suggestByContext(...) []Suggestion
        -candidatesFor(...) []Candidate
        -finish([]Suggestion) []Suggestion
    }

    class Classifier {
        <<interface>>
        +Classify(ctx, Record, []Candidate) ([]Judgement, error)
    }

    class Suggestion {
        +string Tag
        +float64 Confidence
        +string Tier
        +string Reason
    }

    class Result {
        +[]Suggestion Suggestions
        +string Tier
    }

    Knowledge "1" o-- "*" TagStat
    Knowledge ..> Candidate : 作る
    Engine --> Knowledge
    Engine --> Classifier
    Engine ..> Result : 返す
    Result "1" o-- "*" Suggestion
    Engine ..> Record : 受け取る
```

**`Classifier` が境目です。** nil でも動きます（逐語一致と近傍による判定だけになる）。
LLM を使う実装は `internal/classify` にあり、`suggest` はそれを知りません。

### 段階を表す定数

| 定数 | 値 | 意味 |
| --- | --- | --- |
| `TierRule` | `rule` | 設定に書かれたルールで決まった |
| `TierTextMatch` | `text_match` | 本文が過去の記録と逐語一致した |
| `TierContext` | `context` | 近くの記録や語の一致から決まった |
| `TierLLM` | `llm` | LLM が判定した |
| `TierNone` | `none` | どの段階でも決まらなかった（提案0個） |

`TierNone` は失敗ではありません。**「タグを付けない」という正常な答え**です。

## 3. 全体の配線（`internal/app`）

```mermaid
classDiagram
    class App {
        +config.Config Config
        +gkillclient.Client Client
        +store.Store Store
        +suggest.Classifier Classifier
        +ModelLister Models
        +slog.Logger Logger
        +func() time.Time Now
        +UserID() string
        +Analyze(ctx) (AnalyzeReport, error)
        +Benchmark(ctx, BenchmarkOptions) (BenchmarkReport, error)
        +BuildInitialConfig(ctx) (Config, InitReport, error)
        -fetchRecords(ctx) []Record
        -selectCandidates(ctx, []Record) []Record
        -resolveRepNames(ctx) []string
    }

    class AnalyzeReport {
        +int LearnedRecords
        +int CandidateRecords
        +int SuggestedRecords
        +int NoSuggestionRecords
        +int StoredSuggestions
        +int SkippedByVerdict
        +time.Duration Elapsed
    }

    class ModelLister {
        <<interface>>
        +ListModels(ctx) ([]string, error)
    }

    App ..> AnalyzeReport : 返す
    App --> ModelLister
```

**`App` は1人ぶんです。** `Client` が持つセッションはある利用者のものなので、
取れる記録もその人のリポジトリに限られます。複数人を扱うときは人数ぶん作り、
`Store` だけを共有して行を `USER_ID` で分けます。

`AnalyzeReport` に入るのは**件数と所要時間だけ**です。記録の中身は入りません。

## 4. 認証（`internal/gkillauth`）

```mermaid
classDiagram
    class Verifier {
        -string accountDBPath
        -slog.Logger logger
        +Verify(ctx, userID, credential) (bool, DenyReason, error)
        +EnabledUserIDs(ctx) ([]string, error)
    }

    class SessionProvider {
        -string configDir
        -string userID
        -string device
        -string sessionID
        -time.Time expiresAt
        +UserID() string
        +SessionID(ctx) (string, error)
        +Invalidate(ctx)
        +Close(ctx)
        -issue(ctx) (string, time.Time, error)
        -deleteSession(ctx, string)
    }

    class ServerSettings {
        +string Device
        +bool EnableTLS
        +string CertFile
        +string KeyFile
        +string BaseURL
        +EnsureTLSFilesExist() error
    }

    class DenyReason {
        <<enumeration>>
        DenyAccountNotFound
        DenyAccountDisabled
        DenyPasswordResetting
        DenyWrongPassword
    }

    Verifier ..> DenyReason : 返す
    Verifier ..> GkillAccountDAO : 使う
    SessionProvider ..> GkillLoginSessionDAO : 使う
    ServerSettings ..> GkillServerConfigDAO : 使う

    class GkillAccountDAO {
        <<gkill 本体>>
        +GetAccount(ctx, userID) Account
        +GetAllAccounts(ctx) []Account
    }
    class GkillLoginSessionDAO {
        <<gkill 本体>>
        +AddLoginSession(ctx, LoginSession) bool
        +DeleteLoginSession(ctx, sessionID) bool
    }
    class GkillServerConfigDAO {
        <<gkill 本体>>
        +GetAllServerConfigs(ctx) []ServerConfig
    }
```

`DenyReason` は**利用者に返しません**。応答は常に同じ文面で、理由はログにだけ残します。
返すとアカウントの存在や状態を外から探れてしまうためです。

## 5. gkill との通信（`internal/gkillclient`）

```mermaid
classDiagram
    class SessionSource {
        <<interface>>
        +UserID() string
        +SessionID(ctx) (string, error)
        +Invalidate(ctx)
    }

    class Client {
        -string baseURL
        -string localeName
        -http.Client httpClient
        -SessionSource sessions
        +UserID() string
        +BaseURL() string
        +EnsureSession(ctx) (string, error)
        +FetchKyous(ctx, FindQuery, FetchOptions) ([]Kyou, error)
        +GetAllTagNames(ctx) ([]string, error)
        +GetAllRepNames(ctx) ([]string, error)
        +AddTag(ctx, Tag) (bool, error)
        +FetchThumb(ctx, repName, fileName, size) (Image, error)
        -callAuthed(ctx, path, build) ([]byte, error)
        -postRaw(ctx, path, body) ([]byte, error)
        -wrapTransportError(path, err) error
    }

    class APIError {
        +string Path
        +[]GkillError Errors
        +Error() string
        +HasCode(string) bool
    }

    Client --> SessionSource
    Client ..> APIError : 返す
    SessionProvider ..|> SessionSource : 実装する
```

**`Client` は「どうやってセッションを手に入れるか」を知りません。**
`SessionSource` 越しに受け取るので、この層は認証の仕組みから独立しています。
テストでは固定値を返す実装を差し込みます。

## 6. 保存先（`internal/store`）

```mermaid
classDiagram
    class Store {
        -sql.DB db
        +PutSuggestion(ctx, userID, Suggestion) (bool, error)
        +ListPending(ctx, userID) ([]Suggestion, error)
        +CountPending(ctx, userID) (int, error)
        +Decide(ctx, userID, suggestionID, Decision, time) error
        +MarkNoTagNeeded(ctx, userID, targetID, time) error
        +DecidedTargetIDs(ctx, userID) (map, error)
        +MarkEvaluated(ctx, userID, targetID, tier, time) error
        +EvaluatedTargetIDs(ctx, userID) (map, error)
        +ClearSuggestions(ctx, userID) error
        -hasAnyVerdict(ctx, userID, id, targetID) (bool, error)
        -migrate(ctx) error
    }

    class Decision {
        <<enumeration>>
        DecisionApproved
        DecisionRejected
    }

    Store ..> Decision
```

**すべてのメソッドが `userID` を取ります。** 空を渡すと `ErrEmptyUserID` で弾かれます。
空を許すと、その行はどの利用者からも見えない迷子になるか、逆に条件次第で他人に見えます。

## 7. 確認画面（`internal/websrv`）

```mermaid
classDiagram
    class Server {
        -map~string,App~ apps
        -fs.FS frontend
        -authenticator auth
        -bool serveTLS
        -map~string,map~ imageIndex
        -map~string,bool~ analyzing
        +Handler() http.Handler
        +Serve(ctx, ServeOptions) error
        -appFor(userID) App
        -userOf(r) (string, bool)
        -requireAuth(authedHandler) http.HandlerFunc
        -handleLogin/Logout/Session(...)
        -handleSuggestions/Decide/Analyze/Thumb(...)
    }

    class authenticator {
        -Verifier verifier
        -map~string,session~ sessions
        -map~string,[]time.Time~ attempts
        +issue(userID) (string, error)
        +userOf(token) (string, bool)
        +revoke(token)
        +allowAttempt(remoteAddr) bool
    }

    class session {
        +string userID
        +time.Time expiresAt
    }

    Server --> authenticator
    authenticator "1" o-- "*" session
    Server --> Verifier
```

`imageIndex` と `analyzing` が**利用者ごとの map** になっているのが要点です。
索引を共有すると、記録IDを知っているだけで他人の一覧に載った写真を引けてしまいます。

## 8. 確認画面のフロント（TypeScript）

```mermaid
classDiagram
    class App_vue {
        +is_checked: Ref~boolean~
        +is_authenticated: Ref~boolean~
        +logged_in_user_id: Ref~string~
        +is_analyzable: Ref~boolean~
        +refresh_session()
    }

    class LoginView {
        +user_id: Ref~string~
        +password: Ref~string~
        +submit()
    }

    class TagSuggestionPage {
        +props: user_id, is_analyzable
    }

    class useTagSuggestionPage {
        +records: Ref~SuggestionRecord[]~
        +focused_index: Ref~number~
        +selected_tags: Ref~Set~
        +load()
        +run_analyze()
        +confirm()
        +reject()
        +toggle_tag(tag)
        +add_manual_tag()
        +sign_out()
    }

    class api_ts {
        <<module>>
        +fetch_session() SessionState
        +login(user_id, password)
        +logout()
        +fetch_suggestions() SuggestionsResponse
        +decide(target_id, approve_tags)
        +analyze()
        -password_sha256(password) string
    }

    App_vue --> LoginView
    App_vue --> TagSuggestionPage
    TagSuggestionPage --> useTagSuggestionPage
    useTagSuggestionPage --> api_ts
    LoginView --> api_ts
```

画面の動きは `useTagSuggestionPage` に寄せ、`.vue` は見た目だけを持ちます
（gkill 本体のコンポーザブルと同じ形）。詳細は
[frontend-architecture.md](frontend-architecture.md) を参照してください。
