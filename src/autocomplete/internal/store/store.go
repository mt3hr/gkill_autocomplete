// Package store は提案と、それに対する人間の判定を保存する。
//
// この保存先には性質の違う2種類が同居している。
//
//	提案(SUGGESTION)     … 派生データ。消しても解析し直せば戻る。
//	人間の判定(VERDICT)  … 再生成できない。「この記録にこのタグは違う」は
//	                       他のどこにも無い情報で、失うと却下したはずの提案が
//	                       永久に出続けることになる。
//
// そのため保存先はリポジトリの外に置き、キャッシュとして消してよい場所には
// 置かない。派生だけ捨てたいときは ClearSuggestions を使う。
//
// # 利用者ごとの分離
//
// すべての表が USER_ID を持ち、すべての読み書きで絞る。gkill では
// 同じ人でもアカウントごとに別のリポジトリを持つので、混ざると
// 「他人の記録の本文」が画面に出てしまう。
//
// 主キーを (USER_ID, ID) の複合にしているのは、**同じ記録を複数の
// アカウントが見られる**ため。あるアカウントが別のアカウントの
// リポジトリをまとめて抱えている構成は普通にあり、その場合
// 同じ記録IDが両方に現れる。ID だけを主キーにすると、
// 片方の判定がもう片方を上書きしてしまう。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	// pure Go の SQLite ドライバ。CGO を要らなくするために使う。
	_ "modernc.org/sqlite"
)

// Decision は人間が下した判定。
type Decision string

const (
	// DecisionApproved はタグを付けることを承認した。
	DecisionApproved Decision = "approved"
	// DecisionRejected はその提案を退けた。
	DecisionRejected Decision = "rejected"
)

// NoTagNeeded は「この記録にタグは要らない」という記録単位の判定。
//
// 記録の一定割合は、意図的にタグを付けないまま残される。これを覚えておかないと、
// 同じ記録を毎回蒸し返してしまう。
const NoTagNeeded = "no_tag_needed"

// ErrEmptyUserID は利用者IDが空のまま保存先を触ろうとしたことを表す。
//
// 空を許すと、その行はどの利用者からも見えない迷子になるか、
// 逆に条件次第で他人に見えてしまう。必ず弾く。
var ErrEmptyUserID = errors.New("user id is empty")

// Suggestion は提案1件。
type Suggestion struct {
	// ID は (対象の記録ID, タグ名) から決まる決定的な値。
	ID string
	// TagID は承認したときに gkill へ書き込むタグのID。これも決定的。
	TagID string

	TargetID string
	Tag      string

	Confidence float64
	// Tier はどの段階で決まったか。利用者に判断の根拠を示すために持つ。
	Tier string
	// Reason は人向けの短い説明。記録の本文をそのまま入れないこと。
	Reason string

	RepName     string
	DataType    string
	RelatedTime time.Time

	SuggestedAt time.Time
}

// Store は提案と判定の保存先。
type Store struct {
	db *sql.DB
}

// Open は保存先を開く。無ければ作る。
func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("error at make store dir: %w", err)
	}

	// busy_timeout は解析中に確認画面から読まれても待てるようにするため。
	dsn := "file:" + path + "?_pragma=busy_timeout(6000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("error at open store %q: %w", path, err)
	}

	store := &Store{db: db}
	if err := store.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// Close は保存先を閉じる。
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("error at close store: %w", err)
	}
	return nil
}

func (s *Store) migrate(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS SUGGESTION (
			USER_ID      TEXT NOT NULL,
			ID           TEXT NOT NULL,
			TAG_ID       TEXT NOT NULL,
			TARGET_ID    TEXT NOT NULL,
			TAG          TEXT NOT NULL,
			CONFIDENCE   REAL NOT NULL,
			TIER         TEXT NOT NULL,
			REASON       TEXT NOT NULL,
			REP_NAME     TEXT NOT NULL,
			DATA_TYPE    TEXT NOT NULL,
			RELATED_TIME TEXT NOT NULL,
			SUGGESTED_AT TEXT NOT NULL,
			PRIMARY KEY (USER_ID, ID)
		)`,
		`CREATE INDEX IF NOT EXISTS INDEX_SUGGESTION_TARGET ON SUGGESTION (USER_ID, TARGET_ID)`,
		`CREATE INDEX IF NOT EXISTS INDEX_SUGGESTION_RELATED_TIME ON SUGGESTION (USER_ID, RELATED_TIME DESC)`,

		// 人間の判定。再生成できないので決して自動で消さない。
		`CREATE TABLE IF NOT EXISTS VERDICT (
			USER_ID    TEXT NOT NULL,
			ID         TEXT NOT NULL,
			TARGET_ID  TEXT NOT NULL,
			TAG        TEXT NOT NULL,
			DECISION   TEXT NOT NULL,
			DECIDED_AT TEXT NOT NULL,
			PRIMARY KEY (USER_ID, ID)
		)`,
		`CREATE INDEX IF NOT EXISTS INDEX_VERDICT_TARGET ON VERDICT (USER_ID, TARGET_ID)`,

		// 記録単位の「タグは要らない」。こちらも人間の判定なので消さない。
		`CREATE TABLE IF NOT EXISTS RECORD_VERDICT (
			USER_ID    TEXT NOT NULL,
			TARGET_ID  TEXT NOT NULL,
			DECISION   TEXT NOT NULL,
			DECIDED_AT TEXT NOT NULL,
			PRIMARY KEY (USER_ID, TARGET_ID)
		)`,

		// 判定済みの記録。派生データ。
		// 同じ記録に対して LLM を何度も呼ばないためだけに持つ。
		`CREATE TABLE IF NOT EXISTS EVALUATION (
			USER_ID      TEXT NOT NULL,
			TARGET_ID    TEXT NOT NULL,
			EVALUATED_AT TEXT NOT NULL,
			TIER         TEXT NOT NULL,
			PRIMARY KEY (USER_ID, TARGET_ID)
		)`,
	}

	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("error at migrate store: %w", err)
		}
	}
	return nil
}

// PutSuggestion は提案を保存する。
//
// 既に人間が判定している提案と、「タグは要らない」と判定された記録に対する
// 提案は保存しない。却下したものを蒸し返さないため。
// 保存したときだけ stored=true を返す。
func (s *Store) PutSuggestion(ctx context.Context, userID string, suggestion Suggestion) (stored bool, err error) {
	if err := requireUserID(userID); err != nil {
		return false, err
	}

	decided, err := s.hasAnyVerdict(ctx, userID, suggestion.ID, suggestion.TargetID)
	if err != nil {
		return false, err
	}
	if decided {
		return false, nil
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO SUGGESTION
			(USER_ID, ID, TAG_ID, TARGET_ID, TAG, CONFIDENCE, TIER, REASON, REP_NAME, DATA_TYPE, RELATED_TIME, SUGGESTED_AT)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		userID,
		suggestion.ID,
		suggestion.TagID,
		suggestion.TargetID,
		suggestion.Tag,
		suggestion.Confidence,
		suggestion.Tier,
		suggestion.Reason,
		suggestion.RepName,
		suggestion.DataType,
		formatTime(suggestion.RelatedTime),
		formatTime(suggestion.SuggestedAt),
	)
	if err != nil {
		return false, fmt.Errorf("error at put suggestion: %w", err)
	}
	return true, nil
}

// hasAnyVerdict はその提案、またはその記録に人間の判定があるかを返す。
func (s *Store) hasAnyVerdict(ctx context.Context, userID string, suggestionID string, targetID string) (bool, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM VERDICT WHERE USER_ID = ? AND ID = ?)
			+ (SELECT COUNT(*) FROM RECORD_VERDICT WHERE USER_ID = ? AND TARGET_ID = ?)`,
		userID, suggestionID, userID, targetID)

	count := 0
	if err := row.Scan(&count); err != nil {
		return false, fmt.Errorf("error at check verdict: %w", err)
	}
	return count > 0, nil
}

// ListPending はまだ判定されていない提案を新しい順に返す。
func (s *Store) ListPending(ctx context.Context, userID string) ([]Suggestion, error) {
	if err := requireUserID(userID); err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT ID, TAG_ID, TARGET_ID, TAG, CONFIDENCE, TIER, REASON, REP_NAME, DATA_TYPE, RELATED_TIME, SUGGESTED_AT
		FROM SUGGESTION
		WHERE USER_ID = ?
		  AND ID NOT IN (SELECT ID FROM VERDICT WHERE USER_ID = ?)
		  AND TARGET_ID NOT IN (SELECT TARGET_ID FROM RECORD_VERDICT WHERE USER_ID = ?)
		ORDER BY RELATED_TIME DESC, TAG ASC`,
		userID, userID, userID)
	if err != nil {
		return nil, fmt.Errorf("error at list pending suggestions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	collected := []Suggestion{}
	for rows.Next() {
		suggestion := Suggestion{}
		relatedTime := ""
		suggestedAt := ""
		if err := rows.Scan(
			&suggestion.ID,
			&suggestion.TagID,
			&suggestion.TargetID,
			&suggestion.Tag,
			&suggestion.Confidence,
			&suggestion.Tier,
			&suggestion.Reason,
			&suggestion.RepName,
			&suggestion.DataType,
			&relatedTime,
			&suggestedAt,
		); err != nil {
			return nil, fmt.Errorf("error at scan suggestion: %w", err)
		}
		suggestion.RelatedTime = parseTime(relatedTime)
		suggestion.SuggestedAt = parseTime(suggestedAt)
		collected = append(collected, suggestion)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error at read suggestions: %w", err)
	}
	return collected, nil
}

// Decide は提案1件に対する人間の判定を記録する。
//
// 判定は再生成できないので、上書きはするが自動では消さない。
func (s *Store) Decide(ctx context.Context, userID string, suggestionID string, decision Decision, decidedAt time.Time) error {
	if err := requireUserID(userID); err != nil {
		return err
	}
	if decision != DecisionApproved && decision != DecisionRejected {
		return fmt.Errorf("知らない判定です: %q", decision)
	}

	row := s.db.QueryRowContext(ctx,
		`SELECT TARGET_ID, TAG FROM SUGGESTION WHERE USER_ID = ? AND ID = ?`,
		userID, suggestionID)
	targetID := ""
	tagName := ""
	if err := row.Scan(&targetID, &tagName); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("提案が見つかりません: %s", suggestionID)
		}
		return fmt.Errorf("error at find suggestion: %w", err)
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO VERDICT (USER_ID, ID, TARGET_ID, TAG, DECISION, DECIDED_AT)
		VALUES (?, ?, ?, ?, ?, ?)`,
		userID, suggestionID, targetID, tagName, string(decision), formatTime(decidedAt))
	if err != nil {
		return fmt.Errorf("error at save verdict: %w", err)
	}
	return nil
}

// MarkNoTagNeeded は「この記録にタグは要らない」を記録する。
//
// 併せて、その記録に対する未判定の提案をすべて却下扱いにする。
func (s *Store) MarkNoTagNeeded(ctx context.Context, userID string, targetID string, decidedAt time.Time) error {
	if err := requireUserID(userID); err != nil {
		return err
	}

	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("error at begin transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	if _, err := transaction.ExecContext(ctx, `
		INSERT OR REPLACE INTO RECORD_VERDICT (USER_ID, TARGET_ID, DECISION, DECIDED_AT)
		VALUES (?, ?, ?, ?)`,
		userID, targetID, NoTagNeeded, formatTime(decidedAt)); err != nil {
		return fmt.Errorf("error at save record verdict: %w", err)
	}

	if _, err := transaction.ExecContext(ctx, `
		INSERT OR REPLACE INTO VERDICT (USER_ID, ID, TARGET_ID, TAG, DECISION, DECIDED_AT)
		SELECT USER_ID, ID, TARGET_ID, TAG, ?, ? FROM SUGGESTION
		WHERE USER_ID = ? AND TARGET_ID = ?`,
		string(DecisionRejected), formatTime(decidedAt), userID, targetID); err != nil {
		return fmt.Errorf("error at reject suggestions of record: %w", err)
	}

	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("error at commit: %w", err)
	}
	return nil
}

// DecidedTargetIDs は既に人間が判定を下した記録のIDを返す。
//
// 解析側がこれを見て、蒸し返さないよう候補から外す。
func (s *Store) DecidedTargetIDs(ctx context.Context, userID string) (map[string]struct{}, error) {
	if err := requireUserID(userID); err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT TARGET_ID FROM RECORD_VERDICT WHERE USER_ID = ?
		UNION
		SELECT TARGET_ID FROM VERDICT WHERE USER_ID = ?`,
		userID, userID)
	if err != nil {
		return nil, fmt.Errorf("error at list decided targets: %w", err)
	}
	defer func() { _ = rows.Close() }()

	decided := map[string]struct{}{}
	for rows.Next() {
		targetID := ""
		if err := rows.Scan(&targetID); err != nil {
			return nil, fmt.Errorf("error at scan decided target: %w", err)
		}
		decided[targetID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error at read decided targets: %w", err)
	}
	return decided, nil
}

// MarkEvaluated はその記録を判定済みとして覚える。
//
// 同じ記録に対して LLM を二度呼ばないための派生データ。
// 提案が0個だった記録も含めて記録する。
func (s *Store) MarkEvaluated(ctx context.Context, userID string, targetID string, tier string, evaluatedAt time.Time) error {
	if err := requireUserID(userID); err != nil {
		return err
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO EVALUATION (USER_ID, TARGET_ID, EVALUATED_AT, TIER)
		VALUES (?, ?, ?, ?)`,
		userID, targetID, formatTime(evaluatedAt), tier)
	if err != nil {
		return fmt.Errorf("error at mark evaluated: %w", err)
	}
	return nil
}

// EvaluatedTargetIDs は判定済みの記録のIDを返す。
func (s *Store) EvaluatedTargetIDs(ctx context.Context, userID string) (map[string]struct{}, error) {
	if err := requireUserID(userID); err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx, `SELECT TARGET_ID FROM EVALUATION WHERE USER_ID = ?`, userID)
	if err != nil {
		return nil, fmt.Errorf("error at list evaluated targets: %w", err)
	}
	defer func() { _ = rows.Close() }()

	evaluated := map[string]struct{}{}
	for rows.Next() {
		targetID := ""
		if err := rows.Scan(&targetID); err != nil {
			return nil, fmt.Errorf("error at scan evaluated target: %w", err)
		}
		evaluated[targetID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error at read evaluated targets: %w", err)
	}
	return evaluated, nil
}

// ClearSuggestions は提案と判定済みの記録を消す。
//
// 消すのは派生データだけで、人間の判定(VERDICT / RECORD_VERDICT)には触らない。
// 判定を消してしまうと、却下したはずの提案が次の解析で全部復活する。
//
// 消すのは指定した利用者のぶんだけ。他の利用者の提案は残す。
func (s *Store) ClearSuggestions(ctx context.Context, userID string) error {
	if err := requireUserID(userID); err != nil {
		return err
	}

	for _, statement := range []string{
		`DELETE FROM SUGGESTION WHERE USER_ID = ?`,
		`DELETE FROM EVALUATION WHERE USER_ID = ?`,
	} {
		if _, err := s.db.ExecContext(ctx, statement, userID); err != nil {
			return fmt.Errorf("error at clear suggestions: %w", err)
		}
	}
	return nil
}

// CountPending は未判定の提案の件数を返す。
func (s *Store) CountPending(ctx context.Context, userID string) (int, error) {
	if err := requireUserID(userID); err != nil {
		return 0, err
	}

	row := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM SUGGESTION
		WHERE USER_ID = ?
		  AND ID NOT IN (SELECT ID FROM VERDICT WHERE USER_ID = ?)
		  AND TARGET_ID NOT IN (SELECT TARGET_ID FROM RECORD_VERDICT WHERE USER_ID = ?)`,
		userID, userID, userID)
	count := 0
	if err := row.Scan(&count); err != nil {
		return 0, fmt.Errorf("error at count pending suggestions: %w", err)
	}
	return count, nil
}

// requireUserID は利用者IDが指定されていることを確かめる。
func requireUserID(userID string) error {
	if userID == "" {
		return ErrEmptyUserID
	}
	return nil
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func parseTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}
