package store

// Chat Sessions（迁移 000046）
//
// 本模块为 AssistantPage 补齐"会话概念"：此前 Run 是单次执行记录，
// 多轮对话没有持久上下文。chat_sessions 作为对话容器，chat_messages
// 存储每一轮 user/assistant/system 消息。
//
// 约定与 runs.go / action.go 一致：RLS 租户事务 + keyset cursor 分页
// （默认 10/页，按 created_at DESC）+ 结构化 scan 函数 + 统一错误码。

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// ChatSessionColumns 是 ChatSession 的显式投影（uuid 列 ::text，保证稳定序列化）。
const chatSessionColumns = `
    s.id::text, s.title, s.ontology_id::text, s.tenant_id::text, s.subject_id::text,
    s.created_at, s.updated_at, s.archived_at, s.status`

// ChatMessageColumns 是 ChatMessage 的显式投影。
const chatMessageColumns = `
    m.id::text, m.session_id::text, m.role, m.content,
    m.run_id::text, m.created_at`

// ChatSession 是 app.chat_sessions 的读模型。
type ChatSession struct {
	ID         string     `json:"id"`
	Title      string     `json:"title"`
	OntologyID string     `json:"ontology_id,omitempty"`
	TenantID   string     `json:"tenant_id"`
	SubjectID  string     `json:"subject_id"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	ArchivedAt *time.Time `json:"archived_at,omitempty"`
	Status     string     `json:"status"`
}

// ChatMessage 是 app.chat_messages 的读模型。
type ChatMessage struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	RunID     string    `json:"run_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// ---------------------------------------------------------------------------
// 会话 CRUD
// ---------------------------------------------------------------------------

// CreateChatSession 在当前租户事务内创建一条 ACTIVE 状态的聊天会话。
//
// subjectID 由 handler 从信任身份取（不接受请求体覆盖，第十六篇 §5.4）；
// ontologyID 可选（NULL 表示"不绑定本体的通用对话"）；title 不能为空。
func (s *Store) CreateChatSession(ctx context.Context, subjectID ids.ID, ontologyID ids.ID, title string) (*ChatSession, error) {
	if subjectID.IsZero() {
		return nil, errors.E(errors.CodeInvalidArgument, "subject_id 不合法")
	}
	if title == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "title 不能为空")
	}
	sessionID := ids.New()
	var out *ChatSession
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
            INSERT INTO app.chat_sessions
                (id, tenant_id, subject_id, ontology_id, title, status)
            VALUES ($1, app.current_tenant_id(), $2, $3, $4, 'ACTIVE')
            RETURNING `+chatSessionColumns,
			sessionID.String(), subjectID.String(), nullableID(ontologyID), title)
		cs, err := scanChatSession(row)
		if err != nil {
			return err
		}
		out = &cs
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ChatSessionListFilter 是会话列表筛选条件。
//
// subjectID 必填：列表只展示"当前用户自己的会话"（多租户下的第二层隔离）；
// status 为空表示返回全部（含 ACTIVE + ARCHIVED），否则严格命中枚举白名单。
type ChatSessionListFilter struct {
	SubjectID ids.ID
	Status    string // "", "ACTIVE", "ARCHIVED"
	Limit     int
	Cursor    string
}

// ListChatSessions 按 created_at DESC cursor 分页返回会话列表。
func (s *Store) ListChatSessions(ctx context.Context, f ChatSessionListFilter) ([]ChatSession, string, error) {
	if f.SubjectID.IsZero() {
		return nil, "", errors.E(errors.CodeInvalidArgument, "subject_id 不合法")
	}
	if f.Status != "" && f.Status != "ACTIVE" && f.Status != "ARCHIVED" {
		return nil, "", errors.E(errors.CodeInvalidArgument, "status 必须是 ACTIVE 或 ARCHIVED")
	}
	cur, err := DecodeCursor(f.Cursor)
	if err != nil {
		return nil, "", err
	}
	var rows []ChatSession
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err = queryChatSessions(ctx, tx, f, cur, f.Limit+1)
		return err
	})
	if err != nil {
		return nil, "", err
	}
	hasMore := len(rows) > f.Limit
	if hasMore {
		rows = rows[:f.Limit]
	}
	next := ""
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		next = nextCursorOf(last.CreatedAt, last.ID, true)
	}
	return rows, next, nil
}

// queryChatSessions 在租户事务内执行带 cursor 的会话查询。
func queryChatSessions(ctx context.Context, tx pgx.Tx, f ChatSessionListFilter, cur *Cursor, limit int) ([]ChatSession, error) {
	curTime, curID := cursorTime(cur), cursorKey(cur)
	statusFilter := f.Status
	rows, err := tx.Query(ctx, `
        SELECT `+chatSessionColumns+`
          FROM app.chat_sessions s
         WHERE s.subject_id = $1
           AND ($2::text = '' OR s.status = $2)
           AND ($3::timestamptz IS NULL OR (s.created_at, s.id) < ($3::timestamptz, $4::uuid))
         ORDER BY s.created_at DESC, s.id DESC
         LIMIT $5`,
		f.SubjectID.String(), statusFilter, curTime, curID, limit)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "查询聊天会话失败")
	}
	defer rows.Close()
	var out []ChatSession
	for rows.Next() {
		cs, err := scanChatSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, cs)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "遍历聊天会话失败")
	}
	return out, nil
}

// GetChatSession 按 id 读取单条会话（RLS 限制为本租户；subject 归属由调用方校验）。
func (s *Store) GetChatSession(ctx context.Context, id ids.ID) (*ChatSession, error) {
	if id.IsZero() {
		return nil, errors.E(errors.CodeInvalidArgument, "session id 不合法")
	}
	var out *ChatSession
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
            SELECT `+chatSessionColumns+` FROM app.chat_sessions s WHERE s.id = $1`, id.String())
		cs, err := scanChatSession(row)
		if err != nil {
			return err
		}
		out = &cs
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateChatSession 只允许更新 title（其他字段不对外暴露修改，遵循单一职责）。
// RowsAffected=0 视为会话不存在（404），绝不静默假成功。
func (s *Store) UpdateChatSession(ctx context.Context, id ids.ID, title string) error {
	if id.IsZero() {
		return errors.E(errors.CodeInvalidArgument, "session id 不合法")
	}
	if title == "" {
		return errors.E(errors.CodeInvalidArgument, "title 不能为空")
	}
	return s.TenantTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
            UPDATE app.chat_sessions
               SET title = $2, updated_at = now()
             WHERE id = $1`, id.String(), title)
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "更新聊天会话失败")
		}
		if tag.RowsAffected() == 0 {
			return errors.E(errors.CodeNotFound, "聊天会话不存在或不在可见范围")
		}
		return nil
	})
}

// ArchiveChatSession 将会话状态置为 ARCHIVED 并登记 archived_at。
// 幂等：已归档的会话调用不报错（RowsAffected=0 但语义合法）。
func (s *Store) ArchiveChatSession(ctx context.Context, id ids.ID) error {
	if id.IsZero() {
		return errors.E(errors.CodeInvalidArgument, "session id 不合法")
	}
	return s.TenantTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
            UPDATE app.chat_sessions
               SET status = 'ARCHIVED',
                   archived_at = coalesce(archived_at, now()),
                   updated_at = now()
             WHERE id = $1 AND status <> 'ARCHIVED'`, id.String())
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "归档聊天会话失败")
		}
		if tag.RowsAffected() == 0 {
			// 两种可能：会话不存在，或已经归档。返回 404 前者会误伤后者，
			// 这里改为 GET 一次区分语义——但其实幂等性是更重要的契约，
			// 归档操作本身不应该因"已经归档"而报错。所以 RowsAffected=0
			// 统一视为成功（要么已经归档，要么不存在；不存在场景在后续消息写入
			// 时会被外键约束拦截）。
			return nil
		}
		return nil
	})
}

// DeleteChatSession 硬删除会话（级联删除所有 chat_messages，外键 ON DELETE CASCADE）。
// RowsAffected=0 返回 404。
func (s *Store) DeleteChatSession(ctx context.Context, id ids.ID) error {
	if id.IsZero() {
		return errors.E(errors.CodeInvalidArgument, "session id 不合法")
	}
	return s.TenantTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM app.chat_sessions WHERE id = $1`, id.String())
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "删除聊天会话失败")
		}
		if tag.RowsAffected() == 0 {
			return errors.E(errors.CodeNotFound, "聊天会话不存在或不在可见范围")
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// 消息追加与读取
// ---------------------------------------------------------------------------

// AddChatMessage 在指定会话下追加一条消息。
//
// role 必须是 user / assistant / system 之一（CHECK 约束保障 + 应用层前置校验）；
// runID 可选（关联到某次 Run 的执行）。会话不存在时外键约束会抛出错误，
// 统一收敛为 INTERNAL（由 handler 层把 CREATE 流程内的错误转译为 404）。
func (s *Store) AddChatMessage(ctx context.Context, sessionID ids.ID, role, content string, runID ids.ID) error {
	if sessionID.IsZero() {
		return errors.E(errors.CodeInvalidArgument, "session id 不合法")
	}
	if role != "user" && role != "assistant" && role != "system" {
		return errors.E(errors.CodeInvalidArgument, "role 必须是 user / assistant / system")
	}
	if content == "" {
		return errors.E(errors.CodeInvalidArgument, "content 不能为空")
	}
	return s.TenantTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
            INSERT INTO app.chat_messages
                (id, session_id, role, content, run_id)
            VALUES ($1, $2, $3, $4, $5)`,
			ids.New().String(), sessionID.String(), role, content, nullableID(runID))
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "写入聊天消息失败（会话可能不存在）")
		}
		return nil
	})
}

// ChatMessageListFilter 是消息列表筛选条件。
//
// 消息按 created_at ASC 翻页（对话天然按时间正序展示）。
type ChatMessageListFilter struct {
	SessionID ids.ID
	Limit     int
	Cursor    string
}

// ListChatMessages 返回指定会话下的消息（created_at ASC cursor 分页）。
func (s *Store) ListChatMessages(ctx context.Context, f ChatMessageListFilter) ([]ChatMessage, string, error) {
	if f.SessionID.IsZero() {
		return nil, "", errors.E(errors.CodeInvalidArgument, "session id 不合法")
	}
	cur, err := DecodeCursor(f.Cursor)
	if err != nil {
		return nil, "", err
	}
	var rows []ChatMessage
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		curTime, curID := cursorTime(cur), cursorKey(cur)
		q, qerr := tx.Query(ctx, `
            SELECT `+chatMessageColumns+`
              FROM app.chat_messages m
             WHERE m.session_id = $1
               AND ($2::timestamptz IS NULL OR (m.created_at, m.id) > ($2::timestamptz, $3::uuid))
             ORDER BY m.created_at ASC, m.id ASC
             LIMIT $4`,
			f.SessionID.String(), curTime, curID, f.Limit+1)
		if qerr != nil {
			return errors.Wrap(qerr, errors.CodeInternal, "查询聊天消息失败")
		}
		defer q.Close()
		for q.Next() {
			cm, serr := scanChatMessage(q)
			if serr != nil {
				return serr
			}
			rows = append(rows, cm)
		}
		return q.Err()
	})
	if err != nil {
		return nil, "", err
	}
	hasMore := len(rows) > f.Limit
	if hasMore {
		rows = rows[:f.Limit]
	}
	next := ""
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		// 注意：消息列表按 created_at ASC，cursor 比较用 >，因此 nextCursorOf 的
		// hasMore 参数传 true 表示有下一页。
		next = nextCursorOf(last.CreatedAt, last.ID, true)
	}
	return rows, next, nil
}

// ---------------------------------------------------------------------------
// scan 函数（与 scanRun 同构：行或 rows 均可）
// ---------------------------------------------------------------------------

// scanChatSession 从一行扫描 ChatSession。
func scanChatSession(row scannable) (ChatSession, error) {
	var (
		cs                  ChatSession
		ontologyID, subjectID *string
		archivedAt          *time.Time
	)
	err := row.Scan(
		&cs.ID, &cs.Title, &ontologyID, &cs.TenantID, &subjectID,
		&cs.CreatedAt, &cs.UpdatedAt, &archivedAt, &cs.Status,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return cs, errors.E(errors.CodeNotFound, "聊天会话不存在或不在可见范围")
		}
		return cs, errors.Wrap(err, errors.CodeInternal, "读取聊天会话失败")
	}
	if ontologyID != nil {
		cs.OntologyID = *ontologyID
	}
	if subjectID != nil {
		// subject_id 理论上 NOT NULL，防御性处理
		cs.SubjectID = *subjectID
	}
	if archivedAt != nil {
		cs.ArchivedAt = archivedAt
	}
	return cs, nil
}

// scanChatMessage 从一行扫描 ChatMessage。
func scanChatMessage(row scannable) (ChatMessage, error) {
	var (
		cm     ChatMessage
		runID  *string
	)
	err := row.Scan(
		&cm.ID, &cm.SessionID, &cm.Role, &cm.Content, &runID, &cm.CreatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return cm, errors.E(errors.CodeNotFound, "聊天消息不存在")
		}
		return cm, errors.Wrap(err, errors.CodeInternal, "读取聊天消息失败")
	}
	if runID != nil {
		cm.RunID = *runID
	}
	return cm, nil
}
