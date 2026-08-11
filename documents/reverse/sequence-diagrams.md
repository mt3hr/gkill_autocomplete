# シーケンス図

主要な処理の流れです。図は Mermaid で書かれています。閲覧方法は [README.md](README.md) を参照してください。

**時系列ではなく手順の分岐を見たい**場合は [activity-diagrams.md](activity-diagrams.md)、
**通しの使い方**を見たい場合は [scenario.md](scenario.md) を参照してください。

## 1. 設定の生成（`gkill_autocomplete init`）

```mermaid
sequenceDiagram
    actor User as 利用者
    participant CLI as gkill_autocomplete
    participant Cfg as $GKILL_HOME/configs
    participant Gkill as gkill
    participant FS as $GKILL_AUTOCOMPLETE_HOME

    User->>CLI: init --user <利用者ID>
    CLI->>FS: 設定ファイルの有無を確認
    Note over CLI,FS: 既にあれば、解析する前に中止する

    CLI->>Cfg: account.db のスキーマ版を確認
    Note over CLI,Cfg: 現行版でなければ何もせず止まる
    CLI->>Cfg: account_state.db にセッションを書く
    Note over CLI,Cfg: パスワードは要らない

    CLI->>Gkill: 記録を取得(範囲を絞らず、上限まで)
    Gkill-->>CLI: 記録
    CLI->>CLI: 文脈ごとに使われ方を集計
    CLI->>CLI: 人が選んでいる文脈を選ぶ
    CLI->>CLI: 機械が付けているタグを除外に回す
    CLI->>FS: 設定ファイルを書き出す(所有者のみ読める権限)
    CLI->>Cfg: 発行したセッションを消す
    CLI-->>User: 要約を表示
```

## 2. 解析（`gkill_autocomplete --once`）

```mermaid
sequenceDiagram
    actor User as 利用者
    participant CLI as gkill_autocomplete
    participant Gkill as gkill
    participant Store as 保存先
    participant LLM as ローカルLLM

    User->>CLI: --once
    CLI->>CLI: 設定を読んで検証
    Note over CLI: 接続先がループバックでなければここで止まる

    CLI->>Gkill: 記録を取得(学習範囲)
    Note over CLI,Gkill: 学習範囲は候補範囲を含むので取得は1回
    Gkill-->>CLI: 記録

    CLI->>CLI: 学習(文脈ごとの統計・本文の索引・参考例)
    CLI->>Store: 判定済みの記録を問い合わせ
    Store-->>CLI: 判定済みの記録
    CLI->>CLI: 候補を絞る

    loop 候補の記録ごと
        CLI->>CLI: 設定のルール
        alt ルールで決まった
            CLI->>CLI: 提案を確定
        else 本文が過去と逐語一致
            CLI->>CLI: 提案を確定
        else 近傍の記録や語が一致
            CLI->>CLI: 提案を確定
        else どれでも決まらない
            CLI->>LLM: 候補タグの当否を尋ねる
            Note over CLI,LLM: 渡すのは本文か写真と候補タグ名だけ
            LLM-->>CLI: 候補ごとの当否
        end
        CLI->>Store: 提案を保存(判定済みなら保存しない)
        CLI->>Store: 判定済みの印を付ける
    end

    CLI-->>User: 件数を表示
```

## 3. 確認と承認

```mermaid
sequenceDiagram
    actor User as 利用者
    participant Web as 確認画面
    participant Srv as gkill_autocomplete
    participant Store as 保存先
    participant Gkill as gkill

    User->>Web: 画面を開く(ログイン済み)
    Web->>Srv: 未判定の提案を要求
    Srv->>Store: 未判定の提案(ログインした利用者のぶんだけ)
    Store-->>Srv: 提案
    Srv->>Gkill: 記録の中身を取得(IDで指定)
    Note over Srv,Gkill: 中身は保存せず、そのつど取り直す
    Gkill-->>Srv: 記録
    Srv->>Srv: 記録IDと写真の場所の対応を更新
    Srv-->>Web: 記録ごとにまとめた提案

    opt 写真がある
        Web->>Srv: 写真を要求(記録のIDのみ)
        Srv->>Gkill: サムネイルを取得(クッキー認証)
        Gkill-->>Srv: 画像
        Srv-->>Web: 画像(ディスクには残さない)
    end

    User->>Web: タグを選んで確定
    Web->>Srv: 承認するタグ
    loop 選んだタグごと
        Srv->>Gkill: タグを書き込む
        alt 同じ識別子のタグが既にある
            Gkill-->>Srv: 既にある
            Note over Srv: 手で消したタグを蘇らせない
        else 新規
            Gkill-->>Srv: 書き込んだ
        end
        Srv->>Store: 承認として記録
    end
    loop 選ばなかった提案ごと
        Srv->>Store: 却下として記録
    end
    Srv-->>Web: 承認と却下の件数
```

## 4. タグ不要の判定

```mermaid
sequenceDiagram
    actor User as 利用者
    participant Web as 確認画面
    participant Srv as gkill_autocomplete
    participant Store as 保存先

    User->>Web: 何も選ばずに確定(または x)
    Web->>Srv: 承認するタグ = 空
    Srv->>Store: この記録はタグ不要
    Srv->>Store: この記録の提案をすべて却下
    Note over Store: どちらも再生成できない情報として残す
    Srv-->>Web: 完了

    Note over Srv,Store: 次に解析しても、この記録には提案を出さない
```

## 5. セッションの発行と取り直し

コマンドラインは資格情報を受け取りません。gkill を叩くためのセッションは、
gkill の設定ディレクトリへ直接書いて発行します。

```mermaid
sequenceDiagram
    participant CLI as gkill_autocomplete
    participant DB as account_state.db
    participant Gkill as gkill

    Note over CLI: --user で渡された利用者ID だけを持っている

    CLI->>DB: セッションを1行書く(1週間)
    Note over DB: ApplicationName は "gkill" 固定<br/>違うと gkill の認証が弾く
    DB-->>CLI: SessionID

    CLI->>Gkill: 要求(セッション付き)
    alt 認証エラー
        Gkill-->>CLI: ERR000013 / ERR000373 など
        CLI->>DB: いまのセッションを消す
        CLI->>DB: 新しいセッションを書く
        DB-->>CLI: SessionID
        CLI->>Gkill: 要求(1回だけやり直す)
        Gkill-->>CLI: 応答
    else 正常
        Gkill-->>CLI: 応答
    end

    Note over CLI: 期限が近づいたら発行し直す<br/>(確認画面は何時間も上がったまま)

    CLI->>DB: 終了時に発行したセッションを消す
```

**gkill の `/api/login` は一度も叩きません。** 叩くと gkill 側のログイン回数
（IP毎・15分に10回）を消費してしまうためです。

## 5.1 確認画面のログイン

```mermaid
sequenceDiagram
    participant User as 利用者
    participant Web as 確認画面
    participant Srv as gkill_autocomplete
    participant DB as account.db

    User->>Web: 利用者IDとパスワード
    Note over Web: ブラウザの中で SHA-256 にする<br/>平文はここから出ない
    Web->>Srv: user_id, password_sha256

    Srv->>Srv: 回数制限(15分に5回)を先に計上
    Srv->>DB: アカウントを引く
    DB-->>Srv: アカウント

    alt 居ない / 無効 / リセット中 / パスワード違い
        Note over Srv: 理由はログにだけ残す
        Srv-->>Web: 401(どれも同じ文面)
    else 一致
        Note over Srv: gkill の VerifyPassword を<br/>そのまま使う(Argon2id)
        Srv-->>Web: クッキー(利用者IDを持つ)
    end

    Note over Srv: 以後、見えるのはその利用者の提案だけ
```

## 6. 判定の段階（フローチャート）

```mermaid
flowchart TD
    Start[判定したい記録] --> Denied{設定で提案を<br/>止めている?}
    Denied -->|はい| Zero[提案0個]
    Denied -->|いいえ| Rule{設定のルールに<br/>当てはまる?}
    Rule -->|はい| Done[提案を確定]
    Rule -->|いいえ| Exact{本文が過去と<br/>逐語一致?}
    Exact -->|はい| Done
    Exact -->|いいえ| Cand{候補タグが<br/>ある?}
    Cand -->|いいえ| Zero
    Cand -->|はい| Context{近傍の記録や<br/>語が一致?}
    Context -->|はい| Done
    Context -->|いいえ| HasLLM{LLMが<br/>使える?}
    HasLLM -->|いいえ| Zero
    HasLLM -->|はい| Ask[候補ごとに当否を尋ねる]
    Ask --> Done
    Done --> Threshold{確信度が<br/>閾値以上?}
    Threshold -->|いいえ| Zero
    Threshold -->|はい| Emit[提案として保存]

    Zero --> Mark[判定済みの印を付ける]
    Emit --> Mark
```

提案0個は失敗ではありません。記録の一定割合は、意図的にタグを付けないまま残されます。
