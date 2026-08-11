# アクティビティ図

処理の手順です。「なぜそうするか」は [design-philosophy.md](design-philosophy.md)、
「何を守るか」は [requirements.md](requirements.md) にあります。

## 1. 起動

**アカウントDBのスキーマを確かめる位置が要点です。** ここを飛ばすと、
gkill 側の自動移行で全アカウントのパスワードが無効化されます。

```mermaid
flowchart TD
    Start([起動]) --> User{--user が<br/>指定されている?}
    User -->|いいえ| ErrUser[利用者を指定するよう案内して終わる]
    User -->|はい| Config[設定を読む]

    Config --> Valid{検証を通る?}
    Valid -->|いいえ| ErrConfig[問題を全部まとめて出して終わる]
    Valid -->|はい| Home[gkill のホームを決める<br/>明示 &gt; GKILL_HOME &gt; HOME/gkill]

    Home --> Schema[account.db のスキーマ版を読む<br/>DAO を通さない]
    Schema --> SchemaOK{現行版?}
    SchemaOK -->|いいえ| ErrSchema[先に gkill を起動するよう案内して終わる]
    SchemaOK -->|はい| Server[server_config.db を読む]

    Server --> Device{有効なデバイスがある?}
    Device -->|いいえ| ErrDevice[有効なデバイスが無いと出して終わる]
    Device -->|はい| Store[保存先を開く]

    Store --> Loop[利用者ごとに繰り返す]
    Loop --> Session[セッションを1つ発行してみる]
    Session --> SessionOK{発行できた?}
    SessionOK -->|いいえ| ErrSession[アカウントが無い/無効と出して終わる]
    SessionOK -->|はい| Client[クライアントと App を作る]
    Client --> More{次の利用者がいる?}
    More -->|はい| Loop
    More -->|いいえ| Mode{--once ?}

    Mode -->|はい| Analyze([解析へ])
    Mode -->|いいえ| Serve([確認画面へ])

    style Schema fill:#fdd,stroke:#333
    style ErrSchema fill:#fdd,stroke:#333
```

セッションを起動時に1つ試すのは、**アカウントの間違いを解析の前に見つける**ためです。
30分ぶんの記録を読んでから「そんなアカウントは無い」と言われては徒労になります。

## 2. 解析

```mermaid
flowchart TD
    Start([解析]) --> Fetch[学習範囲ぶんの記録を取る<br/>ページングで1000件ずつ]
    Fetch --> Limit{上限に達した?}
    Limit -->|はい| Warn[警告を出す<br/>学習が欠けている]
    Limit -->|いいえ| Learn
    Warn --> Learn[履歴から学習する]

    Learn --> Select[判定の対象を選ぶ]
    Select --> Sort[近傍を引くため時刻順に並べる]
    Sort --> Each{対象が残っている?}

    Each -->|いいえ| Report([件数を報告して終わる])
    Each -->|はい| Cancelled{中断された?}
    Cancelled -->|はい| Abort([中断して終わる])
    Cancelled -->|いいえ| Neighbors[前後 N 分の記録を集める]
    Neighbors --> Judge[段階的に判定する]
    Judge --> Failed{判定に失敗した?}

    Failed -->|はい| CountFailed[失敗として数え<br/>評価済みの印は付けない]
    CountFailed --> Each

    Failed -->|いいえ| Zero{提案が0個?}

    Zero -->|はい| MarkNone[提案なしとして数える]
    Zero -->|いいえ| Save[提案を保存する]

    Save --> Skip{すでに判定がある?}
    Skip -->|はい| Skipped[見送りとして数える]
    Skip -->|いいえ| Stored[保存したとして数える]

    MarkNone --> Mark[評価済みの印を付ける]
    Skipped --> Mark
    Stored --> Mark
    Mark --> Each
```

**評価済みの印は、提案が0個だった記録にも付けます。** 付けないと、
毎回同じ記録に LLM を呼ぶことになります。

**判定に失敗した1件で解析全体を落としません。** LLM の時間切れ・応答の解釈失敗・
写真が取れない、はどれも実際に起きます。落とすと後ろに並んだ何十件もが処理されず、
しかも失敗した記録は評価済みにならないので、**何度やり直しても同じ場所で止まり続けます**。
飛ばして次へ進み、件数だけを報告します。

失敗した記録に評価済みの印を付けないのは意図的です。次の解析でやり直されます。

## 3. 判定（段階を降りる）

**上で決まれば下へ行きません。** 本文が過去の記録と逐語一致するなら、
それ以上何かを推測する必要はありません。

```mermaid
flowchart TD
    Start([判定したい記録]) --> Deny[設定のルールで<br/>禁止されたタグを集める]
    Deny --> DenyAll{全部禁止?}
    DenyAll -->|はい| Zero([提案0個])

    DenyAll -->|いいえ| Rule[段階0: 設定のルール]
    Rule --> RuleHit{当てはまった?}
    RuleHit -->|はい| Finish

    RuleHit -->|いいえ| Exact[段階1: 本文の逐語一致]
    Exact --> ExactHit{一致した?}
    ExactHit -->|はい| FinishExact[その本文に付いていた<br/>すべてのタグを提案]
    FinishExact --> Finish

    ExactHit -->|いいえ| Cand[候補タグを絞る<br/>実績が min_examples 以上]
    Cand --> CandEmpty{候補が0個?}
    CandEmpty -->|はい| Zero

    CandEmpty -->|いいえ| Context[段階2: 近傍・語の一致]
    Context --> ContextHit{一致した?}
    ContextHit -->|はい| Dampen1[習慣で割り引く]
    Dampen1 --> Finish

    ContextHit -->|いいえ| HasLLM{判定器がある?}
    HasLLM -->|いいえ| Zero

    HasLLM -->|はい| LLM[段階3: LLM に候補ごとの<br/>当否を尋ねる]
    LLM --> Filter[候補に無い名前は捨てる]
    Filter --> Dampen2[習慣で割り引く]
    Dampen2 --> Finish

    Finish[閾値で足切り<br/>同じタグは最も高いものだけ残す] --> Result([提案 0個以上])

    style Zero fill:#dfd,stroke:#333
    style Result fill:#dfd,stroke:#333
```

緑は正常な終わりです。**0個は失敗ではありません。**

### 習慣で割り引く

```mermaid
flowchart LR
    A[もとの確信度] --> C[割り引いた確信度]
    B["1 − その文脈でタグを付けない割合"] --> C
```

推測から出た確信度は当てになりません。実測では、写真の保管庫に対する LLM の判定が
**9件すべて 1.0** を返し、後付けの理由が付いていました。自己申告をそのまま
閾値にかけても何も濾せません。

一方「その場所でタグを付けているか」は履歴から数えられる事実です。
ほとんどタグを付けない場所では、よほど強い証拠が無い限り黙るのが正しい動きです。

**段階0と1は割り引きません。** 推測ではなく直接の証拠だからです。

## 4. 承認

```mermaid
flowchart TD
    Start([確定を押した]) --> Auth{ログイン済み?}
    Auth -->|いいえ| E401[401 を返す]
    Auth -->|はい| Target{解析の対象?}
    Target -->|いいえ| E403[403 を返す]

    Target -->|はい| List[自分の未判定の提案を引く]
    List --> Each{その記録の提案が残っている?}

    Each -->|はい| Selected{選ばれている?}
    Selected -->|いいえ| Reject[却下として記録する]
    Selected -->|はい| Write[gkill へタグを書き込む]

    Write --> Exists{同じIDのタグが<br/>既にある?}
    Exists -->|はい| Note[書き込まない<br/>手で消したタグを蘇らせない]
    Exists -->|いいえ| Wrote[書き込んだ]
    Note --> Approve[承認として記録する]
    Wrote --> Approve

    Reject --> Each
    Approve --> Each

    Each -->|いいえ| Zero{1つも承認しなかった?}
    Zero -->|はい| NoTag[この記録はタグ不要として記録する]
    Zero -->|いいえ| Count
    NoTag --> Count[残りの件数を数えて返す]
    Count --> End([完了])
```

**選ばれなかった提案は、同じ記録のぶんがまとめて却下になります。**
そうしないと同じ記録が何度も出てきます。

画面側は押した瞬間に一覧から外し、失敗したら元の位置へ戻します。

## 5. 写真の中継

```mermaid
flowchart TD
    Start([GET /thumb?target=ID]) --> Auth{ログイン済み?}
    Auth -->|いいえ| E401[401]
    Auth -->|はい| Target{target がある?}
    Target -->|いいえ| E400[400]

    Target -->|はい| Index[その利用者の索引を引く]
    Index --> Found{索引にある?}
    Found -->|いいえ| E404[404<br/>他人の写真もここで止まる]

    Found -->|はい| Fetch[gkill からサムネイルを取る]
    Fetch --> OK{取れた?}
    OK -->|いいえ| E502[502]
    OK -->|はい| Relay[Cache-Control: no-store を付けて返す<br/>ディスクには残さない]
    Relay --> End([完了])

    style Index fill:#bbf,stroke:#333
```

**索引にしか無い経路です。** リポジトリ名やファイル名を外から渡させないので、
一覧に出ていない場所のファイルは読めません。索引は利用者ごとなので、
記録IDを知っているだけでは他人の写真は引けません。

## 6. 設定の生成（`init`）

```mermaid
flowchart TD
    Start([init]) --> Exists{設定ファイルが<br/>すでにある?}
    Exists -->|はい| Err[上書きしないと伝えて終わる]
    Exists -->|いいえ| Fetch[稼働中の gkill を読む]

    Fetch --> Group[文脈ごとに集計する]
    Group --> Each{文脈が残っている?}

    Each -->|はい| Judge{記録20件以上 かつ<br/>2種類以上のタグ かつ<br/>タグ無しが95%未満?}
    Judge -->|はい| Pick[対象にする]
    Judge -->|いいえ| Machine{1種類が95%以上 かつ<br/>タグ無しが5%未満?}
    Machine -->|はい| Exclude[機械付与とみなして<br/>除外パターンに入れる]
    Machine -->|いいえ| Ignore[対象にしない]

    Pick --> Each
    Exclude --> Each
    Ignore --> Each

    Each -->|いいえ| Models[LLM に使えるモデルを尋ねる]
    Models --> ModelsOK{答えた?}
    ModelsOK -->|はい| Choose[名前から視覚用・本文用を選ぶ]
    ModelsOK -->|いいえ| Empty[モデルの欄を空にする<br/>失敗にはしない]

    Choose --> Save[設定を書き出す]
    Empty --> Save
    Save --> Report([選んだ結果を画面に出す])
```

**2種類以上のタグを使い分けている**ことを条件にしているのは、
そこで人の選択が起きている印だからです。1種類しか使われていない文脈は、
機械が一律に付けているか、そもそも選択の余地がありません。

## 7. 精度の計測（`benchmark`）

```mermaid
flowchart TD
    Start([benchmark --from --to]) --> Fetch[計測期間と、それ以前の記録を取る]
    Fetch --> Learn[計測期間より前だけで学習する]
    Learn --> Each{計測期間の記録が残っている?}

    Each -->|はい| Pop{母集団に入る?}
    Pop -->|いいえ| Skip[飛ばす<br/>機械が付けたタグの記録など]
    Pop -->|はい| Judge[判定する]
    Judge --> Compare[実際に付いているタグと比べる]
    Compare --> Each
    Skip --> Each

    Each -->|いいえ| Report([適合率・再現率を出す])

    style Learn fill:#bbf,stroke:#333
```

**学習を計測期間より前に限るのが要点です。** そうしないと自分の答えを見て
当てることになり、数字が意味を持ちません。

**保存先にも gkill にも一切書き込みません。**
