package identity

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/pgxutil"
)

// Organization 是组织树节点（对应 iam.organizations）。
// ParentID 为全零表示根组织。
type Organization struct {
	ID        ids.ID
	TenantID  ids.ID
	ParentID  ids.ID
	OrgCode   string
	Name      string
	OrgType   string
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ClosureRow 是组织闭包的一行（对应 iam.org_closure）。
// depth=0 表示组织对自身；depth>0 表示跨层祖先-后代关系。
type ClosureRow struct {
	TenantID     ids.ID
	AncestorID   ids.ID
	DescendantID ids.ID
	Depth        int
}

// OrgRepository 抽象组织树与闭包表的持久化。
// 约定：所有查询按 tenant_id 过滤（跨租户即不存在），
// 写操作（InsertOrgTree/MoveOrgSubtree）在实现内部保证原子性。
type OrgRepository interface {
	// InsertOrgTree 原子插入组织及其闭包行（含 depth=0 自环行）。
	InsertOrgTree(ctx context.Context, org Organization, closure []ClosureRow) error
	// MoveOrgSubtree 原子执行组织移动：更新 parent_id，
	// 删除涉及子树内任一组织的全部旧闭包行，写入重建后的新闭包行。
	MoveOrgSubtree(ctx context.Context, tenantID, orgID, newParentID ids.ID, touchedIDs []ids.ID, insertRows []ClosureRow) error
	// SelectOrg 按租户查询组织；不存在返回 errRepoNotFound。
	SelectOrg(ctx context.Context, tenantID, orgID ids.ID) (Organization, error)
	// SelectAncestors 返回 orgID 的全部祖先闭包行（含 depth=0 自身），按 depth 升序。
	SelectAncestors(ctx context.Context, tenantID, orgID ids.ID) ([]ClosureRow, error)
	// SelectDescendants 返回 orgID 的全部后代闭包行（含 depth=0 自身），按 depth 升序。
	SelectDescendants(ctx context.Context, tenantID, orgID ids.ID) ([]ClosureRow, error)
	// SelectSubtreeClosure 返回以 orgID 为根的子树的完整内部闭包
	// （ancestor 与 descendant 均在子树内，含 depth=0 行）。
	SelectSubtreeClosure(ctx context.Context, tenantID, orgID ids.ID) ([]ClosureRow, error)
}

// OrgService 提供组织树管理能力（创建、移动、祖先/后代展开）。
type OrgService struct {
	Pool *pgxpool.Pool
	repo OrgRepository
}

// NewOrgService 用生产存储（pgxpool）构造服务。
func NewOrgService(pool *pgxpool.Pool) *OrgService {
	return &OrgService{Pool: pool, repo: pgOrgRepository{pool: pool}}
}

// Create 在租户内创建组织。parentID 为全零表示创建根组织。
// 闭包行在插入时一次算全：自身 depth=0，加上父组织全部祖先的 depth+1 行。
func (s *OrgService) Create(ctx context.Context, tenantID, parentID ids.ID, code, name, orgType string) (ids.ID, error) {
	if tenantID.IsZero() {
		return ids.ID{}, errors.E(errors.CodeValidationFailed, "tenantID 不能为空")
	}
	if code == "" {
		return ids.ID{}, errors.E(errors.CodeValidationFailed, "org_code 不能为空")
	}
	if name == "" {
		return ids.ID{}, errors.E(errors.CodeValidationFailed, "org name 不能为空")
	}
	if orgType == "" {
		orgType = "DEPARTMENT"
	}

	closure := []ClosureRow{{TenantID: tenantID}}
	// 父组织存在性校验同时拒绝跨租户父节点（查询按 tenant_id 过滤）。
	if !parentID.IsZero() {
		if _, err := s.repo.SelectOrg(ctx, tenantID, parentID); err != nil {
			if notFound(err) {
				return ids.ID{}, errors.E(errors.CodeNotFound, "父组织不存在或不属于该租户")
			}
			return ids.ID{}, errors.Wrap(err, errors.CodeInternal, "查询父组织失败")
		}
		ancestors, err := s.repo.SelectAncestors(ctx, tenantID, parentID)
		if err != nil {
			return ids.ID{}, errors.Wrap(err, errors.CodeInternal, "展开父组织闭包失败")
		}
		for _, a := range ancestors {
			closure = append(closure, ClosureRow{
				TenantID:     tenantID,
				AncestorID:   a.AncestorID,
				DescendantID: ids.ID{}, // 占位，下面统一回填新组织 ID
				Depth:        a.Depth + 1,
			})
		}
	}

	now := time.Now().UTC()
	org := Organization{
		ID:        ids.New(),
		TenantID:  tenantID,
		ParentID:  parentID,
		OrgCode:   code,
		Name:      name,
		OrgType:   orgType,
		Status:    "ACTIVE",
		CreatedAt: now,
		UpdatedAt: now,
	}
	closure[0].AncestorID = org.ID
	closure[0].DescendantID = org.ID
	for i := range closure {
		if closure[i].DescendantID.IsZero() {
			closure[i].DescendantID = org.ID
		}
	}

	if err := s.repo.InsertOrgTree(ctx, org, closure); err != nil {
		if conflict(err) {
			return ids.ID{}, errors.Ef(errors.CodeConflict, "org_code %q 已存在", code)
		}
		return ids.ID{}, errors.Wrap(err, errors.CodeInternal, "插入组织失败")
	}
	return org.ID, nil
}

// Move 把组织移动到新的父组织下，并在同一事务内重算 org_closure：
// 删除旧祖先对（所有涉及子树内组织的闭包行），按新父组织重建
// （含 depth>0 行）。自环与循环移动（新父是自身后代）直接拒绝。
// 租户身份来自可信上下文；新父组织跨租户时按不存在拒绝。
func (s *OrgService) Move(ctx context.Context, orgID, newParentID ids.ID) error {
	tenantID, err := tenantFromContext(ctx)
	if err != nil {
		return err
	}
	org, err := s.repo.SelectOrg(ctx, tenantID, orgID)
	if err != nil {
		if notFound(err) {
			return errors.Ef(errors.CodeNotFound, "组织 %s 不存在或不属于该租户", orgID)
		}
		return errors.Wrap(err, errors.CodeInternal, "查询组织失败")
	}
	if newParentID == orgID {
		return errors.E(errors.CodeValidationFailed, "禁止自环移动：新父组织不能是自身")
	}

	var newParentAncestors []ClosureRow
	if !newParentID.IsZero() {
		if _, err := s.repo.SelectOrg(ctx, tenantID, newParentID); err != nil {
			if notFound(err) {
				return errors.E(errors.CodeNotFound, "新父组织不存在或不属于该租户")
			}
			return errors.Wrap(err, errors.CodeInternal, "查询新父组织失败")
		}
		// 新父的祖先链（含其自身 depth=0 行）是重建闭包的“新上半段”。
		newParentAncestors, err = s.repo.SelectAncestors(ctx, tenantID, newParentID)
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "展开新父组织闭包失败")
		}
	}

	// 子树内部闭包（含 org 自身 depth=0 与 depth>0 行）。
	subtree, err := s.repo.SelectSubtreeClosure(ctx, tenantID, orgID)
	if err != nil {
		return errors.Wrap(err, errors.CodeInternal, "展开子树闭包失败")
	}

	subtreeIDs := make(map[ids.ID]int, len(subtree)) // descendant -> 相对 org 的深度
	for _, row := range subtree {
		if row.AncestorID == orgID {
			subtreeIDs[row.DescendantID] = row.Depth
		}
	}
	if _, ok := subtreeIDs[newParentID]; ok {
		return errors.E(errors.CodeValidationFailed, "禁止循环移动：新父组织不能是自身的后代")
	}

	touched := make([]ids.ID, 0, len(subtreeIDs))
	for id := range subtreeIDs {
		touched = append(touched, id)
	}

	// 重建闭包：内部行原样保留；每个后代节点挂到新父祖先链下。
	rows := make([]ClosureRow, 0, len(subtree)+len(subtreeIDs)*len(newParentAncestors))
	for _, row := range subtree {
		if _, inSubtree := subtreeIDs[row.AncestorID]; inSubtree {
			rows = append(rows, row) // 纯内部关系不随移动改变
		}
	}
	for descendant, relDepth := range subtreeIDs {
		for _, a := range newParentAncestors {
			rows = append(rows, ClosureRow{
				TenantID:     tenantID,
				AncestorID:   a.AncestorID,
				DescendantID: descendant,
				Depth:        a.Depth + relDepth + 1,
			})
		}
	}

	if err := s.repo.MoveOrgSubtree(ctx, tenantID, org.ID, newParentID, touched, rows); err != nil {
		if notFound(err) {
			return errors.E(errors.CodeNotFound, "组织或新父组织不存在")
		}
		return errors.Wrap(err, errors.CodeInternal, "移动组织失败")
	}
	return nil
}

// Ancestors 返回组织的全部祖先（含自身 depth=0），按 depth 升序。
// 组织不存在（含跨租户）返回 CodeNotFound。
func (s *OrgService) Ancestors(ctx context.Context, tenantID, orgID ids.ID) ([]ClosureRow, error) {
	if _, err := s.repo.SelectOrg(ctx, tenantID, orgID); err != nil {
		if notFound(err) {
			return nil, errors.Ef(errors.CodeNotFound, "组织 %s 不存在或不属于该租户", orgID)
		}
		return nil, errors.Wrap(err, errors.CodeInternal, "查询组织失败")
	}
	rows, err := s.repo.SelectAncestors(ctx, tenantID, orgID)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "查询祖先失败")
	}
	return rows, nil
}

// Descendants 返回组织的后代（含自身 depth=0）。
// maxDepth >= 0 时仅返回 depth <= maxDepth 的行；maxDepth < 0 表示不限制深度。
// 组织不存在（含跨租户）返回 CodeNotFound。
func (s *OrgService) Descendants(ctx context.Context, tenantID, orgID ids.ID, maxDepth int) ([]ClosureRow, error) {
	if _, err := s.repo.SelectOrg(ctx, tenantID, orgID); err != nil {
		if notFound(err) {
			return nil, errors.Ef(errors.CodeNotFound, "组织 %s 不存在或不属于该租户", orgID)
		}
		return nil, errors.Wrap(err, errors.CodeInternal, "查询组织失败")
	}
	rows, err := s.repo.SelectDescendants(ctx, tenantID, orgID)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "查询后代失败")
	}
	if maxDepth < 0 {
		return rows, nil
	}
	filtered := make([]ClosureRow, 0, len(rows))
	for _, r := range rows {
		if r.Depth <= maxDepth {
			filtered = append(filtered, r)
		}
	}
	return filtered, nil
}

// pgOrgRepository 是 OrgRepository 的 PostgreSQL 实现。
// iam.organizations / iam.org_closure 启用 RLS，所有事务先注入租户上下文。
type pgOrgRepository struct {
	pool *pgxpool.Pool
}

func (r pgOrgRepository) InsertOrgTree(ctx context.Context, org Organization, closure []ClosureRow) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := pgxutil.WithTenantTx(ctx, tx, org.TenantID); err != nil {
		return err
	}
	var parentID any
	if !org.ParentID.IsZero() {
		parentID = org.ParentID.String()
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO iam.organizations (id, tenant_id, parent_id, org_code, name, org_type, status, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		org.ID.String(), org.TenantID.String(), parentID, org.OrgCode, org.Name, org.OrgType, org.Status,
		org.CreatedAt, org.UpdatedAt); err != nil {
		if isUniqueViolation(err) {
			return errRepoConflict
		}
		return err
	}
	for _, row := range closure {
		if _, err := tx.Exec(ctx,
			`INSERT INTO iam.org_closure (tenant_id, ancestor_id, descendant_id, depth)
			 VALUES ($1, $2, $3, $4)`,
			row.TenantID.String(), row.AncestorID.String(), row.DescendantID.String(), row.Depth); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (r pgOrgRepository) MoveOrgSubtree(ctx context.Context, tenantID, orgID, newParentID ids.ID, touchedIDs []ids.ID, insertRows []ClosureRow) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := pgxutil.WithTenantTx(ctx, tx, tenantID); err != nil {
		return err
	}
	var parentID any
	if !newParentID.IsZero() {
		parentID = newParentID.String()
	}
	tag, err := tx.Exec(ctx,
		`UPDATE iam.organizations SET parent_id = $3, updated_at = now()
		 WHERE tenant_id = $1 AND id = $2`,
		tenantID.String(), orgID.String(), parentID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errRepoNotFound
	}

	texts := make([]string, len(touchedIDs))
	for i, id := range touchedIDs {
		texts[i] = id.String()
	}
	// 删除旧祖先对：凡 ancestor 或 descendant 落在子树内的闭包行全部清除
	//（含 depth=0 自环行与 depth>0 跨层行），随后按新父重建。
	if _, err := tx.Exec(ctx,
		`DELETE FROM iam.org_closure
		 WHERE tenant_id = $1
		   AND (ancestor_id = ANY($2::uuid[]) OR descendant_id = ANY($2::uuid[]))`,
		tenantID.String(), texts); err != nil {
		return err
	}
	for _, row := range insertRows {
		if _, err := tx.Exec(ctx,
			`INSERT INTO iam.org_closure (tenant_id, ancestor_id, descendant_id, depth)
			 VALUES ($1, $2, $3, $4)
			 ON CONFLICT (tenant_id, ancestor_id, descendant_id) DO UPDATE SET depth = EXCLUDED.depth`,
			row.TenantID.String(), row.AncestorID.String(), row.DescendantID.String(), row.Depth); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (r pgOrgRepository) SelectOrg(ctx context.Context, tenantID, orgID ids.ID) (Organization, error) {
	var org Organization
	err := readInTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			`SELECT id, tenant_id, parent_id, org_code, name, org_type, status, created_at, updated_at
			 FROM iam.organizations WHERE tenant_id = $1 AND id = $2`,
			tenantID.String(), orgID.String())
		var scanErr error
		org, scanErr = scanOrganization(row)
		return scanErr
	})
	return org, err
}

func (r pgOrgRepository) SelectAncestors(ctx context.Context, tenantID, orgID ids.ID) ([]ClosureRow, error) {
	var out []ClosureRow
	err := readInTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		rows, qErr := tx.Query(ctx,
			`SELECT tenant_id, ancestor_id, descendant_id, depth
			 FROM iam.org_closure WHERE tenant_id = $1 AND descendant_id = $2
			 ORDER BY depth`,
			tenantID.String(), orgID.String())
		if qErr != nil {
			return qErr
		}
		defer rows.Close()
		var scanErr error
		out, scanErr = scanClosureRows(rows)
		return scanErr
	})
	return out, err
}

func (r pgOrgRepository) SelectDescendants(ctx context.Context, tenantID, orgID ids.ID) ([]ClosureRow, error) {
	var out []ClosureRow
	err := readInTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		rows, qErr := tx.Query(ctx,
			`SELECT tenant_id, ancestor_id, descendant_id, depth
			 FROM iam.org_closure WHERE tenant_id = $1 AND ancestor_id = $2
			 ORDER BY depth`,
			tenantID.String(), orgID.String())
		if qErr != nil {
			return qErr
		}
		defer rows.Close()
		var scanErr error
		out, scanErr = scanClosureRows(rows)
		return scanErr
	})
	return out, err
}

func (r pgOrgRepository) SelectSubtreeClosure(ctx context.Context, tenantID, orgID ids.ID) ([]ClosureRow, error) {
	var out []ClosureRow
	err := readInTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		rows, qErr := tx.Query(ctx,
			`SELECT c.tenant_id, c.ancestor_id, c.descendant_id, c.depth
			 FROM iam.org_closure c
			 WHERE c.tenant_id = $1
			   AND c.ancestor_id IN (
			       SELECT descendant_id FROM iam.org_closure WHERE tenant_id = $1 AND ancestor_id = $2)
			   AND c.descendant_id IN (
			       SELECT descendant_id FROM iam.org_closure WHERE tenant_id = $1 AND ancestor_id = $2)`,
			tenantID.String(), orgID.String())
		if qErr != nil {
			return qErr
		}
		defer rows.Close()
		var scanErr error
		out, scanErr = scanClosureRows(rows)
		return scanErr
	})
	return out, err
}

func scanOrganization(row pgx.Row) (Organization, error) {
	var (
		org       Organization
		idText    string
		tenantTxt string
		parentTxt *string
	)
	err := row.Scan(&idText, &tenantTxt, &parentTxt, &org.OrgCode, &org.Name, &org.OrgType, &org.Status, &org.CreatedAt, &org.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return Organization{}, errRepoNotFound
		}
		return Organization{}, err
	}
	if org.ID, err = ids.Parse(idText); err != nil {
		return Organization{}, err
	}
	if org.TenantID, err = ids.Parse(tenantTxt); err != nil {
		return Organization{}, err
	}
	if parentTxt != nil {
		if org.ParentID, err = ids.Parse(*parentTxt); err != nil {
			return Organization{}, err
		}
	}
	return org, nil
}

func scanClosureRows(rows pgx.Rows) ([]ClosureRow, error) {
	out := make([]ClosureRow, 0, 8)
	for rows.Next() {
		var (
			row       ClosureRow
			tenantTxt string
			ancTxt    string
			descTxt   string
		)
		if err := rows.Scan(&tenantTxt, &ancTxt, &descTxt, &row.Depth); err != nil {
			return nil, err
		}
		var err error
		if row.TenantID, err = ids.Parse(tenantTxt); err != nil {
			return nil, err
		}
		if row.AncestorID, err = ids.Parse(ancTxt); err != nil {
			return nil, err
		}
		if row.DescendantID, err = ids.Parse(descTxt); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
