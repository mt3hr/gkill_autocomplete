# 利用シナリオ集

実際の使い方を、端から端まで追います。個々の仕様ではなく
**「そのとき何が呼ばれ、どう動くか」**を通しで見るための資料です。

例に出るタグ名・リポジトリ名はすべて架空のものです。

## シナリオ1: はじめて使う

**状況**: gkill を使い続けて記録が溜まっている。手でタグを付けるのが面倒になってきた。

```mermaid
sequenceDiagram
    actor User as 利用者
    participant CLI as gkill_autocomplete
    participant Cfg as configs/(gkill)
    participant Gkill as gkill
    participant LLM as ローカル LLM

    User->>CLI: init --user sample_user

    CLI->>Cfg: account.db のスキーマ版を読む
    Cfg-->>CLI: 1.1.0（現行版）
    CLI->>Cfg: server_config.db を読む
    Cfg-->>CLI: 有効なデバイス、TLS、宛先
    CLI->>Cfg: account_state.db にセッションを書く
    Note over CLI,Cfg: パスワードは要らない

    CLI->>Gkill: 記録を取る(セッション付き)
    Gkill-->>CLI: 記録
    CLI->>CLI: 文脈ごとに集計する

    CLI->>LLM: 使えるモデルは?
    LLM-->>CLI: モデル名の一覧
    CLI->>CLI: 名前から視覚用・本文用を選ぶ

    CLI->>User: 設定を作りました + 選んだ結果
    CLI->>Cfg: 発行したセッションを消す
```

**ここで何が決まるか**:

- どの文脈を対象にするか（2種類以上のタグを使い分けている場所）
- どのタグを候補から外すか（1種類が場を占めている＝機械付与とみなす）
- どのモデルを使うか

生成された設定を開いて、対象が意図どおりか確かめます。
**実在のタグ名・リポジトリ名が入るのはこのファイルだけ**で、リポジトリの外にあります。

---

## シナリオ2: 夜に解析して、朝に確認する

**状況**: 定期実行に登録して、溜まったぶんを朝まとめて捌きたい。

### 夜（自動）

```mermaid
sequenceDiagram
    participant Cron as タスクスケジューラ
    participant CLI as gkill_autocomplete
    participant Gkill as gkill
    participant LLM as ローカル LLM
    participant Store as 保存先

    Cron->>CLI: --user sample_user --once
    Note over Cron,CLI: 秘密を渡さない

    CLI->>Gkill: 学習範囲ぶんの記録(1000件ずつ)
    Gkill-->>CLI: 記録
    CLI->>CLI: 履歴から学習する

    CLI->>Store: 判定済み・人が判定済みの記録IDを引く
    Store-->>CLI: 除外する記録ID
    CLI->>CLI: 対象を絞る

    loop 対象の記録ごと
        CLI->>CLI: 段階0-2で決まる?
        alt 決まった
            Note over CLI: LLM を呼ばない
        else 決まらない
            CLI->>LLM: 候補タグごとに当否を尋ねる
            LLM-->>CLI: yes/no と理由
            CLI->>CLI: その文脈でタグを付けない割合で割り引く
        end
        CLI->>Store: 提案を保存(閾値を超えたものだけ)
        CLI->>Store: 評価済みの印を付ける
    end

    CLI->>Cron: 件数を出して終わる
```

**LLM を呼ばずに済む記録が多いほど速く終わります。** 定型の記録
（同じ操作を繰り返して生まれるもの）は段階1で片付きます。

### 朝（手動）

```mermaid
sequenceDiagram
    actor User as 利用者
    participant Web as 確認画面
    participant Srv as gkill_autocomplete
    participant Store as 保存先
    participant Gkill as gkill

    User->>Web: https://…:9797/ を開く
    Web->>Srv: GET /api/session
    Srv-->>Web: 未ログイン
    Web->>User: ログイン画面

    User->>Web: 利用者IDとパスワード
    Note over Web: ブラウザの中で SHA-256 にする
    Web->>Srv: POST /api/login
    Srv->>Srv: account.db と照合(Argon2id)
    Srv-->>Web: クッキー

    Web->>Srv: POST /api/suggestions
    Srv->>Store: 自分の未判定を引く
    Store-->>Srv: 提案
    Srv->>Gkill: 記録の中身を取り直す
    Gkill-->>Srv: 本文・タグ・写真の場所
    Srv-->>Web: 記録ごとにまとめた一覧

    loop 1件ずつ
        alt タグを付ける
            User->>Web: 1 を押して Enter
            Web->>Srv: POST /api/decide (approve_tags)
            Srv->>Gkill: POST /api/add_tag
            Srv->>Store: 承認として記録、残りは却下
        else タグは要らない
            User->>Web: x
            Web->>Srv: POST /api/decide (空)
            Srv->>Store: タグ不要として記録
            Note over Store: 二度と提案しない
        end
        Srv-->>Web: 残りの件数
    end
```

**キーボードだけで捌けます。** `x` が最短なのは、
記録の一定割合が元々タグを必要としないためです。

---

## シナリオ3: 写真にタグを付ける

**状況**: 飲み物の写真が溜まっている。中身によってタグを分けたい。

```mermaid
sequenceDiagram
    participant CLI as 解析
    participant Know as 学習結果
    participant Gkill as gkill
    participant LLM as 視覚モデル

    CLI->>Know: この文脈(rep:SampleRep)の候補タグは?
    Know-->>CLI: タグA(実績38件) / タグB(12件)

    CLI->>Gkill: 対象のサムネイル(512x512)
    Gkill-->>CLI: 画像
    CLI->>Know: タグらしい見本の写真を数枚
    Know-->>CLI: 見本(最大 max_few_shot_images 枚)

    CLI->>LLM: 「これはタグA ですか?」+ 対象 + 見本
    LLM-->>CLI: yes / 確信度 1.0
    CLI->>LLM: 「これはタグB ですか?」+ 対象 + 見本
    LLM-->>CLI: no

    Note over CLI: 自己申告の 1.0 は当てにならない
    CLI->>Know: この文脈でタグを付けない割合は?
    Know-->>CLI: 26%
    Note over CLI: 1.0 × (1 - 0.26) = 0.74
    CLI->>CLI: 閾値 0.6 を超えるので提案する
```

**割り引きが効くところです。** 実測では、写真の保管庫に対する LLM の判定が
9件すべて 1.0 を返し、後付けの理由が付いていました。
自己申告をそのまま閾値にかけても何も濾せません。

「その場所でタグを付けているか」は履歴から数えられる事実なので、そちらで補正します。

### 写真を渡すときの注意

| 事項 | 理由 |
| --- | --- |
| サムネイルは各辺 1〜1024 | 範囲外だと gkill は**エラーではなく原本（全画素）**を返す |
| 見本は枚数に注意 | 写真は1枚で千数百トークン。数枚で文脈長を超えて判定が失敗する |
| 渡すのは画像と候補タグ名だけ | ID・リポジトリ名・ファイル名・場所は渡さない |

---

## シナリオ4: 誤った提案が続くので調整する

**状況**: 12件のうち11件が外れた。設定を見直したい。

```mermaid
flowchart TD
    Start([外れが多い]) --> Bench[benchmark で数字を見る]
    Bench --> Which{何が起きている?}

    Which -->|確信度が高いのに外れる| Damp[その文脈でタグを<br/>付けない割合を確かめる]
    Which -->|候補が多すぎる| Cand[max_candidate_tags を絞る<br/>min_examples を上げる]
    Which -->|機械のタグが混ざる| Excl[exclude.tag_patterns に足す]
    Which -->|特定のタグだけ外れる| Rule[rules に never_suggest を書く]

    Damp --> Thresh[scoring.threshold を上げる]
    Cand --> Reset
    Excl --> Reset
    Rule --> Reset
    Thresh --> Reset[reset --user … で提案を捨てる]

    Reset --> Again[--once で付け直す]
    Again --> Check{よくなった?}
    Check -->|いいえ| Bench
    Check -->|はい| Done([完了])

    style Reset fill:#bbf,stroke:#333
```

**`reset` は人間の判定を消しません。** 提案と「評価済みの印」だけを捨てるので、
却下したものが復活することはありません。

`benchmark` は既にタグを付け終えた過去の期間で測ります。学習には計測期間より前の
記録だけを使うので、自分の答えを見て当てることはありません。
**保存先にも gkill にも一切書き込みません。**

---

## シナリオ5: 別の端末から確認する

**状況**: 寝る前にスマートフォンで捌きたい。

```mermaid
sequenceDiagram
    actor User as 利用者
    participant Phone as スマートフォン
    participant PC as PC(gkill と同じ端末)

    Note over PC: gkill_autocomplete --user sample_user<br/>server.listen = 0.0.0.0:9797

    User->>Phone: https://<PCのアドレス>:9797/ を開く

    alt 証明書の名前が合わない
        Phone-->>User: 「この接続ではプライバシーが保護されません」
        Note over User: 証明書に載っている名前で開き直す
    else 発行元が信頼されていない
        Phone-->>User: 同上
        Note over User: cert.cer を端末に入れる
    else 通る
        Phone->>PC: ログイン
        PC-->>Phone: クッキー
        Phone->>User: 提案画面
    end

    User->>Phone: 「ホーム画面に追加」
    Note over Phone: 証明書が信頼されていないと<br/>Service Worker が動かず入れられない
```

**証明書は gkill 本体のものをそのまま使います。** 専用のものを作らないのは、
利用者に証明書をもう1枚信頼させないためです。

そのぶん、**その証明書がどの名前を保証しているか**を確かめる必要があります。
確かめ方は [operations-guide.md](operations-guide.md) の3章にあります。

---

## シナリオ6: 複数のアカウントを扱う

**状況**: 用途で gkill のアカウントを分けている。両方にタグを付けたい。

```mermaid
sequenceDiagram
    participant CLI as gkill_autocomplete
    participant Store as 保存先
    participant Gkill as gkill

    Note over CLI: --user sample_user --user sample_user_all

    CLI->>Gkill: sample_user のセッションで記録を取る
    Gkill-->>CLI: sample_user のリポジトリの記録
    CLI->>Store: USER_ID=sample_user で保存

    CLI->>Gkill: sample_user_all のセッションで記録を取る
    Gkill-->>CLI: sample_user_all のリポジトリの記録<br/>(sample_user のぶんも含む)
    CLI->>Store: USER_ID=sample_user_all で保存

    Note over Store: 同じ記録IDが両方に現れる<br/>主キーが (USER_ID, ID) なので混ざらない
```

**同じ記録を2人が別々に判定できます。** 片方で却下しても、もう片方の未判定は残ります。

画面では、ログインした本人の提案しか見えません。
`--user` に渡していないアカウントでもログインはできますが、
提案は空になり、画面が「対象に含まれていません」と出します。

---

## シナリオ7: gkill を更新したあと

**状況**: gkill を新しい版に上げた。

```mermaid
flowchart TD
    Start([gkill を更新した]) --> Run[gkill_autocomplete --user … を起動]
    Run --> Check{account.db の<br/>スキーマ版は?}

    Check -->|現行版| OK([そのまま動く])
    Check -->|違う| Stop[何もせず止まる<br/>「先に gkill を起動してください」]

    Stop --> StartGkill[gkill を起動する]
    StartGkill --> Migrate[gkill が移行する]
    Migrate --> Run

    style Stop fill:#fdd,stroke:#333
```

**こちらでは移行しません。** gkill のアカウント DAO は旧スキーマの DB を
開いた瞬間に自動移行を走らせ、**全アカウントのパスワードを無効化します**。
先に開いてしまうと、gkill にログインできなくなります。

なお、こちらが import している gkill のコードを上げるときは、
コミットを指定して `go get` します（[dev-setup.md](dev-setup.md) 1章）。

---

## シナリオ8: 何も提案されない

**状況**: 解析は終わったのに提案が0件。

```mermaid
flowchart TD
    Start([提案が0件]) --> Warn{取得の上限に<br/>達した警告が出た?}
    Warn -->|はい| Scope[範囲を絞るか learn_days を縮める]

    Warn -->|いいえ| Cand{判定した記録が0件?}
    Cand -->|はい| Why{なぜ0件?}
    Why -->|期間が短い| Days[candidate_days を伸ばす]
    Why -->|全部タグ付き| Tagged[already_tagged を確かめる]
    Why -->|全部判定済み| Reset[reset で評価済みの印を消す]

    Cand -->|いいえ| Zero{提案なしが多い?}
    Zero -->|はい| Normal([正常。その文脈では<br/>元々タグを付けていない])
    Zero -->|いいえ| Thresh[threshold を下げる<br/>min_examples を下げる]

    style Normal fill:#dfd,stroke:#333
```

**「提案なし」は失敗ではありません。** タグ履歴の無い文脈では候補タグが0個になり、
LLM を呼ばずに終わります。範囲を広げても費用が跳ねないのはこのためです。

時間がかかるのは「タグ履歴があり、かつ未タグの記録が残っている」文脈だけです。
