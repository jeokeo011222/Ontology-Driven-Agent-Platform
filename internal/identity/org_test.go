package identity

import (
	"context"
	"sort"
	"testing"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/tenantctx"
)

// fakeOrgRepo 是 OrgRepository 的内存实现（测试专用，不连接数据库）。
type fakeOrgRepo struct {
	orgs    []Organization
	closure []ClosureRow
}

func (f *fakeOrgRepo) findOrg(tenantID, orgID ids.ID) (Organization, bool) {
	for _, o := range f.orgs {
		if o.TenantID == tenantID && o.ID == orgID {
			return o, true
		}
	}
	return Organization{}, false
}

func (f *fakeOrgRepo) InsertOrgTree(_ context.Context, org Organization, closure []ClosureRow) error {
	for _, o := range f.orgs {
		if o.TenantID == org.TenantID && o.OrgCode == org.OrgCode {
			return errRepoConflict
		}
	}
	f.orgs = append(f.orgs, org)
	f.closure = append(f.closure, closure...)
	return nil
}

func (f *fakeOrgRepo) MoveOrgSubtree(_ context.Context, tenantID, orgID, newParentID ids.ID, touchedIDs []ids.ID, insertRows []ClosureRow) error {
	touched := make(map[ids.ID]bool, len(touchedIDs))
	for _, id := range touchedIDs {
		touched[id] = true
	}
	found := false
	for i := range f.orgs {
		if f.orgs[i].TenantID == tenantID && f.orgs[i].ID == orgID {
			f.orgs[i].ParentID = newParentID
			found = true
			break
		}
	}
	if !found {
		return errRepoNotFound
	}
	kept := f.closure[:0]
	for _, row := range f.closure {
		if row.TenantID == tenantID && (touched[row.AncestorID] || touched[row.DescendantID]) {
			continue // 删除旧祖先对（含 depth=0 与 depth>0）
		}
		kept = append(kept, row)
	}
	f.closure = append(kept, insertRows...)
	return nil
}

func (f *fakeOrgRepo) SelectOrg(_ context.Context, tenantID, orgID ids.ID) (Organization, error) {
	if o, ok := f.findOrg(tenantID, orgID); ok {
		return o, nil
	}
	return Organization{}, errRepoNotFound
}

func (f *fakeOrgRepo) SelectAncestors(_ context.Context, tenantID, orgID ids.ID) ([]ClosureRow, error) {
	var out []ClosureRow
	for _, row := range f.closure {
		if row.TenantID == tenantID && row.DescendantID == orgID {
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Depth < out[j].Depth })
	return out, nil
}

func (f *fakeOrgRepo) SelectDescendants(_ context.Context, tenantID, orgID ids.ID) ([]ClosureRow, error) {
	var out []ClosureRow
	for _, row := range f.closure {
		if row.TenantID == tenantID && row.AncestorID == orgID {
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Depth < out[j].Depth })
	return out, nil
}

func (f *fakeOrgRepo) SelectSubtreeClosure(_ context.Context, tenantID, orgID ids.ID) ([]ClosureRow, error) {
	inSubtree := map[ids.ID]bool{}
	for _, row := range f.closure {
		if row.TenantID == tenantID && row.AncestorID == orgID {
			inSubtree[row.DescendantID] = true
		}
	}
	var out []ClosureRow
	for _, row := range f.closure {
		if row.TenantID == tenantID && inSubtree[row.AncestorID] && inSubtree[row.DescendantID] {
			out = append(out, row)
		}
	}
	return out, nil
}

// ctxWithTenant 构造带可信租户身份的 context。
func ctxWithTenant(tenantID ids.ID) context.Context {
	return tenantctx.WithIdentity(context.Background(), tenantctx.Identity{TenantID: tenantID, RequestID: "test"})
}

// buildTree 构造 root -> a -> b 与 root -> c 两枝，返回各节点 ID。
func buildTree(t *testing.T, svc *OrgService, ctx context.Context, tenantID ids.ID) (root, a, b, c ids.ID) {
	t.Helper()
	var err error
	if root, err = svc.Create(ctx, tenantID, ids.ID{}, "ROOT", "Root", "COMPANY"); err != nil {
		t.Fatalf("create root: %v", err)
	}
	if a, err = svc.Create(ctx, tenantID, root, "A", "Dept A", "DEPARTMENT"); err != nil {
		t.Fatalf("create a: %v", err)
	}
	if b, err = svc.Create(ctx, tenantID, a, "B", "Team B", "TEAM"); err != nil {
		t.Fatalf("create b: %v", err)
	}
	if c, err = svc.Create(ctx, tenantID, root, "C", "Dept C", "DEPARTMENT"); err != nil {
		t.Fatalf("create c: %v", err)
	}
	return root, a, b, c
}

func TestOrgCreateBuildsClosure(t *testing.T) {
	repo := &fakeOrgRepo{}
	svc := &OrgService{repo: repo}
	ctx := context.Background()
	tenant := ids.New()
	root, a, b, _ := buildTree(t, svc, ctx, tenant)

	ancestors, err := svc.Ancestors(ctx, tenant, b)
	if err != nil {
		t.Fatalf("Ancestors(b): %v", err)
	}
	want := []struct {
		id    ids.ID
		depth int
	}{{b, 0}, {a, 1}, {root, 2}}
	if len(ancestors) != len(want) {
		t.Fatalf("ancestors(b) = %d rows, want %d", len(ancestors), len(want))
	}
	for i, w := range want {
		if ancestors[i].AncestorID != w.id || ancestors[i].Depth != w.depth {
			t.Fatalf("ancestor[%d] = (%s, %d), want (%s, %d)", i,
				ancestors[i].AncestorID, ancestors[i].Depth, w.id, w.depth)
		}
	}

	desc, err := svc.Descendants(ctx, tenant, root, -1)
	if err != nil {
		t.Fatalf("Descendants(root): %v", err)
	}
	if len(desc) != 4 {
		t.Fatalf("descendants(root) = %d rows, want 4 (root/a/b/c)", len(desc))
	}

	// depth 过滤：root 的 depth<=1 后代只有 a 与 c（不含 b）。
	shallow, err := svc.Descendants(ctx, tenant, root, 1)
	if err != nil {
		t.Fatalf("Descendants(root, 1): %v", err)
	}
	if len(shallow) != 3 {
		t.Fatalf("descendants(root, depth<=1) = %d rows, want 3", len(shallow))
	}
	for _, row := range shallow {
		if row.DescendantID == b {
			t.Fatal("depth filter leaked deep descendant b")
		}
	}
}

func TestOrgMoveRecomputesClosure(t *testing.T) {
	repo := &fakeOrgRepo{}
	svc := &OrgService{repo: repo}
	tenant := ids.New()
	ctx := ctxWithTenant(tenant)
	root, a, b, c := buildTree(t, svc, ctx, tenant)

	// 把 b 从 a 下移动到 c 下：b 的祖先应变为 c(1) -> root(2)，a 不再是祖先。
	if err := svc.Move(ctx, b, c); err != nil {
		t.Fatalf("Move(b -> c): %v", err)
	}
	ancestors, err := svc.Ancestors(ctx, tenant, b)
	if err != nil {
		t.Fatalf("Ancestors(b) after move: %v", err)
	}
	type pair struct {
		id    ids.ID
		depth int
	}
	want := []pair{{b, 0}, {c, 1}, {root, 2}}
	if len(ancestors) != len(want) {
		t.Fatalf("ancestors(b) after move = %d rows, want %d", len(ancestors), len(want))
	}
	for i, w := range want {
		if ancestors[i].AncestorID != w.id || ancestors[i].Depth != w.depth {
			t.Fatalf("ancestor[%d] after move = (%s, %d), want (%s, %d)",
				i, ancestors[i].AncestorID, ancestors[i].Depth, w.id, w.depth)
		}
	}

	// a 的后代只剩 a 自身（b 已迁走）。
	desc, err := svc.Descendants(ctx, tenant, a, -1)
	if err != nil {
		t.Fatalf("Descendants(a) after move: %v", err)
	}
	if len(desc) != 1 || desc[0].DescendantID != a || desc[0].Depth != 0 {
		t.Fatalf("descendants(a) after move = %+v, want only self", desc)
	}

	// c 的子树应包含 b（depth=1）与 root 视角 depth=2。
	descC, err := svc.Descendants(ctx, tenant, c, -1)
	if err != nil {
		t.Fatalf("Descendants(c): %v", err)
	}
	found := false
	for _, row := range descC {
		if row.DescendantID == b && row.Depth == 1 {
			found = true
		}
	}
	if !found {
		t.Fatal("b missing from c subtree after move")
	}
}

func TestOrgMoveRejectsCycles(t *testing.T) {
	repo := &fakeOrgRepo{}
	svc := &OrgService{repo: repo}
	tenant := ids.New()
	ctx := ctxWithTenant(tenant)
	root, a, b, _ := buildTree(t, svc, ctx, tenant)

	cases := []struct {
		name        string
		orgID       ids.ID
		newParentID ids.ID
		wantCode    string
	}{
		{"self loop", a, a, errors.CodeValidationFailed},
		{"move under own descendant", root, b, errors.CodeValidationFailed},
		{"move a under its child b", a, b, errors.CodeValidationFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := svc.Move(ctx, tc.orgID, tc.newParentID)
			if err == nil {
				t.Fatalf("Move(%s) expected rejection", tc.name)
			}
			if errors.As(err).Code != tc.wantCode {
				t.Fatalf("Move(%s) err = %v, want %s", tc.name, err, tc.wantCode)
			}
		})
	}

	// 被拒绝的移动不得破坏既有闭包。
	ancestors, err := svc.Ancestors(ctx, tenant, b)
	if err != nil {
		t.Fatalf("Ancestors(b): %v", err)
	}
	if len(ancestors) != 3 || ancestors[2].AncestorID != root {
		t.Fatalf("closure corrupted by rejected moves: %+v", ancestors)
	}
}

func TestOrgCrossTenantRejected(t *testing.T) {
	repo := &fakeOrgRepo{}
	svc := &OrgService{repo: repo}
	ctx := context.Background()
	tenantA := ids.New()
	tenantB := ids.New()

	rootA, aA, _, _ := buildTree(t, svc, ctx, tenantA)
	rootB, err := svc.Create(ctx, tenantB, ids.ID{}, "ROOT_B", "Root B", "COMPANY")
	if err != nil {
		t.Fatalf("create rootB: %v", err)
	}

	// Create：父组织属于别的租户 -> NOT_FOUND。
	_, err = svc.Create(ctx, tenantA, rootB, "X", "X", "DEPARTMENT")
	if errors.As(err).Code != errors.CodeNotFound {
		t.Fatalf("cross-tenant parent create err = %v, want NOT_FOUND", err)
	}

	// Move：组织属于别的租户 -> NOT_FOUND。
	ctxA := ctxWithTenant(tenantA)
	if err := svc.Move(ctxA, rootB, aA); errors.As(err).Code != errors.CodeNotFound {
		t.Fatalf("cross-tenant move org err = %v, want NOT_FOUND", err)
	}

	// Move：新父组织属于别的租户 -> NOT_FOUND。
	if err := svc.Move(ctxA, aA, rootB); errors.As(err).Code != errors.CodeNotFound {
		t.Fatalf("cross-tenant new parent err = %v, want NOT_FOUND", err)
	}

	// 查询同样按租户隔离。
	if _, err := svc.Ancestors(ctx, tenantB, rootA); errors.As(err).Code != errors.CodeNotFound {
		t.Fatalf("cross-tenant ancestors err = %v, want NOT_FOUND", err)
	}
}

func TestOrgCreateValidatesInput(t *testing.T) {
	svc := &OrgService{repo: &fakeOrgRepo{}}
	ctx := context.Background()
	tenant := ids.New()

	cases := []struct {
		name     string
		tenantID ids.ID
		code     string
		orgName  string
	}{
		{"zero tenant", ids.ID{}, "C", "N"},
		{"empty code", tenant, "", "N"},
		{"empty name", tenant, "C", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Create(ctx, tc.tenantID, ids.ID{}, tc.code, tc.orgName, "")
			if errors.As(err).Code != errors.CodeValidationFailed {
				t.Fatalf("Create err = %v, want VALIDATION_FAILED", err)
			}
		})
	}
}

func TestOrgCreateDuplicateCodeConflict(t *testing.T) {
	svc := &OrgService{repo: &fakeOrgRepo{}}
	ctx := context.Background()
	tenant := ids.New()
	if _, err := svc.Create(ctx, tenant, ids.ID{}, "DUP", "First", ""); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := svc.Create(ctx, tenant, ids.ID{}, "DUP", "Second", "")
	if errors.As(err).Code != errors.CodeConflict {
		t.Fatalf("duplicate code err = %v, want CONFLICT", err)
	}
}

func TestOrgMoveMissingTenantContext(t *testing.T) {
	svc := &OrgService{repo: &fakeOrgRepo{}}
	err := svc.Move(context.Background(), ids.New(), ids.New())
	if errors.As(err).Code != errors.CodeUnauthenticated {
		t.Fatalf("Move without tenant ctx err = %v, want UNAUTHENTICATED", err)
	}
}
