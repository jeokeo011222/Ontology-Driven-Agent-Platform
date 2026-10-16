package reasoning

// 自定义规则注册/移除：写 reason.rules（kind=CUSTOM、is_builtin=false）。
//
// body 结构校验使用 encoding/json 的 DisallowUnknownFields——**仅允许**
// rule_key/name/kind/premises/conclusion/description 六个字段。规则体是
// 推理语义的承载，未知字段要么是调用方笔误（premise 写成 premises），要么是
// 未来版本的语义扩张（当前版本无法执行）——两者静默吞掉都会造成"看起来
// 注册成功了、推理时规则却不是调用方想的那条"，因此 fail-closed 拒绝。

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/pgxutil"
)

// ruleBodyDTO 是 reason.rules.body 的严格投影（与 proto RuleDef 同构）。
type ruleBodyDTO struct {
	RuleKey     string          `json:"rule_key"`
	Name        string          `json:"name"`
	Kind        string          `json:"kind"`
	Premises    []TriplePattern `json:"premises"`
	Conclusion  []TriplePattern `json:"conclusion"`
	Description string          `json:"description"`
}

// ParseRuleBody 把规则体 JSON 严格解析为 Rule（DisallowUnknownFields）。
// HTTP 与 gRPC 两条注册路径共用本函数，保证校验判据只有一份。
func ParseRuleBody(raw []byte) (Rule, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return Rule{}, errors.E(errors.CodeInvalidArgument, "规则体不能为空")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var dto ruleBodyDTO
	if err := dec.Decode(&dto); err != nil {
		return Rule{}, errors.E(errors.CodeInvalidArgument,
			"规则体非法（仅允许 rule_key/name/kind/premises/conclusion/description 字段）："+err.Error())
	}
	r := Rule{
		RuleKey:     strings.TrimSpace(dto.RuleKey),
		Name:        strings.TrimSpace(dto.Name),
		Kind:        strings.TrimSpace(dto.Kind),
		Premises:    dto.Premises,
		Conclusion:  dto.Conclusion,
		Description: dto.Description,
	}
	if r.Kind == "" {
		r.Kind = KindCUSTOM
	}
	if err := ValidateRule(r); err != nil {
		return Rule{}, err
	}
	return r, nil
}

// ValidateRule 校验规则的结构合法性（内置规则与自定义规则共用同一判据）。
func ValidateRule(r Rule) error {
	if strings.TrimSpace(r.RuleKey) == "" {
		return errors.E(errors.CodeInvalidArgument, "规则 rule_key 不能为空")
	}
	if strings.ContainsAny(r.RuleKey, " \t\r\n") {
		return errors.Ef(errors.CodeInvalidArgument, "规则 rule_key %q 不能含空白字符", r.RuleKey)
	}
	if isNamedVariable(r.RuleKey) {
		return errors.E(errors.CodeInvalidArgument, "规则 rule_key 不能以 \"?\" 开头（与变量词法冲突）")
	}
	switch r.Kind {
	case KindRDFS, KindOWL2RL, KindCUSTOM:
	default:
		return errors.Ef(errors.CodeInvalidArgument, "规则 kind %q 不在枚举内（RDFS | OWL2RL | CUSTOM）", r.Kind)
	}
	if len(r.Premises) == 0 {
		return errors.E(errors.CodeInvalidArgument, "规则至少需要一条前提（premises）")
	}
	if len(r.Conclusion) == 0 {
		return errors.E(errors.CodeInvalidArgument, "规则至少需要一条结论（conclusion）")
	}
	// 前提中的常量位置必须可解析为合法词项。
	for i, p := range r.Premises {
		if err := validatePattern(p, "premises", i, true); err != nil {
			return err
		}
	}
	for i, c := range r.Conclusion {
		// 结论中的匿名变量无从绑定，直接拒绝。
		if err := validatePattern(c, "conclusion", i, false); err != nil {
			return err
		}
	}
	// 结论变量必须全部出现在前提中（否则实例化时无从取值）。
	premiseVars := make(map[string]bool)
	for _, p := range r.Premises {
		for _, lex := range []string{p.Subject, p.Predicate, p.Object} {
			if isNamedVariable(lex) {
				premiseVars[varName(lex)] = true
			}
		}
	}
	for _, c := range r.Conclusion {
		for _, lex := range []string{c.Subject, c.Predicate, c.Object} {
			if isNamedVariable(lex) && !premiseVars[varName(lex)] {
				return errors.Ef(errors.CodeInvalidArgument,
					"结论变量 ?%s 未出现在任何前提中（实例化时无从取值）", varName(lex))
			}
		}
	}
	return nil
}

// validatePattern 校验单个模式：常量位置必须可解析；allowAnonymous=false 时
// 拒绝匿名变量。
func validatePattern(p TriplePattern, where string, i int, allowAnonymous bool) error {
	fields := map[string]string{"subject": p.Subject, "predicate": p.Predicate, "object": p.Object}
	for name, lex := range fields {
		if isNamedVariable(lex) {
			continue
		}
		if isVariable(lex) {
			if !allowAnonymous {
				return errors.Ef(errors.CodeInvalidArgument,
					"%s[%d].%s 为匿名变量：结论模式不允许匿名变量（无法绑定）", where, i, name)
			}
			continue
		}
		if _, err := termFromLexical(lex); err != nil {
			return errors.Ef(errors.CodeInvalidArgument, "%s[%d].%s 非法：%v", where, i, name, err)
		}
	}
	return nil
}

// RuleRejection 是一条规则未被接受/移除的原因。
type RuleRejection struct {
	RuleKey string `json:"rule_key"`
	Reason  string `json:"reason"`
}

// RegisterRules 批量注册自定义规则（upsert 语义：同键覆盖更新）。
// 逐条返回接受/拒绝结果，单条失败不影响其余（调用方多为批量导入场景）。
func (s *Service) RegisterRules(ctx context.Context, tenantID ids.ID, defs []Rule) ([]string, []RuleRejection, error) {
	if tenantID.IsZero() {
		return nil, nil, errors.E(errors.CodeInvalidArgument, "tenant_id 不能为空")
	}
	accepted := make([]string, 0, len(defs))
	rejected := make([]RuleRejection, 0)
	builtins := builtinKeySet()

	err := s.tx(ctx, tenantID, func(tx pgx.Tx) error {
		for _, def := range defs {
			def.RuleKey = strings.TrimSpace(def.RuleKey)
			if def.Kind == "" {
				def.Kind = KindCUSTOM
			}
			if err := ValidateRule(def); err != nil {
				rejected = append(rejected, RuleRejection{RuleKey: def.RuleKey, Reason: err.Error()})
				continue
			}
			if def.Kind != KindCUSTOM {
				rejected = append(rejected, RuleRejection{RuleKey: def.RuleKey,
					Reason: "注册端仅接受 kind=CUSTOM 的自定义规则（内置规则集由代码承载，不可覆盖）"})
				continue
			}
			if builtins[def.RuleKey] {
				rejected = append(rejected, RuleRejection{RuleKey: def.RuleKey,
					Reason: "rule_key 与内置规则冲突"})
				continue
			}
			body, err := json.Marshal(ruleBodyDTO{
				RuleKey:     def.RuleKey,
				Name:        def.Name,
				Kind:        KindCUSTOM,
				Premises:    def.Premises,
				Conclusion:  def.Conclusion,
				Description: def.Description,
			})
			if err != nil {
				rejected = append(rejected, RuleRejection{RuleKey: def.RuleKey,
					Reason: "规则体序列化失败：" + err.Error()})
				continue
			}
			if _, err := tx.Exec(ctx, `
                INSERT INTO reason.rules (id, tenant_id, rule_key, name, kind, body, description, enabled, is_builtin, created_by)
                VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5::jsonb, $6, TRUE, FALSE, $7)
                ON CONFLICT (tenant_id, rule_key) DO UPDATE
                  SET name = EXCLUDED.name,
                      body = EXCLUDED.body,
                      description = EXCLUDED.description,
                      enabled = TRUE,
                      updated_at = now()`,
				ids.New().String(), def.RuleKey, def.Name, KindCUSTOM, string(body), def.Description,
				nil, // created_by：gRPC 注册路径无主体身份，置空
			); err != nil {
				rejected = append(rejected, RuleRejection{RuleKey: def.RuleKey,
					Reason: "规则写入失败：" + err.Error()})
				continue
			}
			accepted = append(accepted, def.RuleKey)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return accepted, rejected, nil
}

// RemoveRules 按键移除自定义规则；内置规则（含启停开关行）拒绝移除。
func (s *Service) RemoveRules(ctx context.Context, tenantID ids.ID, ruleKeys []string) ([]string, []RuleRejection, error) {
	if tenantID.IsZero() {
		return nil, nil, errors.E(errors.CodeInvalidArgument, "tenant_id 不能为空")
	}
	removed := make([]string, 0, len(ruleKeys))
	notRemoved := make([]RuleRejection, 0)

	err := s.tx(ctx, tenantID, func(tx pgx.Tx) error {
		for _, key := range ruleKeys {
			key = strings.TrimSpace(key)
			tag, err := tx.Exec(ctx,
				`DELETE FROM reason.rules WHERE tenant_id = app.current_tenant_id() AND rule_key = $1 AND is_builtin = FALSE`,
				key)
			if err != nil {
				notRemoved = append(notRemoved, RuleRejection{RuleKey: key, Reason: "移除失败：" + err.Error()})
				continue
			}
			if tag.RowsAffected() == 0 {
				// 区分"内置规则"与"不存在"，给出可行动的原因。
				var isBuiltin bool
				err := tx.QueryRow(ctx,
					`SELECT is_builtin FROM reason.rules WHERE tenant_id = app.current_tenant_id() AND rule_key = $1`,
					key).Scan(&isBuiltin)
				if err == nil && isBuiltin {
					notRemoved = append(notRemoved, RuleRejection{RuleKey: key, Reason: "内置规则不可移除（仅可启停）"})
				} else {
					notRemoved = append(notRemoved, RuleRejection{RuleKey: key, Reason: "规则不存在"})
				}
				continue
			}
			removed = append(removed, key)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return removed, notRemoved, nil
}

// tx 在显式租户事务中执行 fn（pgxutil.TenantTx 注入 app.tenant_id，RLS 生效）。
func (s *Service) tx(ctx context.Context, tenantID ids.ID, fn func(pgx.Tx) error) error {
	if s.Pool == nil {
		return errors.E(errors.CodeUnavailable, "推理引擎：数据库连接池未就绪（规则持久化不可用）")
	}
	return pgxutil.TenantTx(ctx, s.Pool, tenantID, fn)
}
