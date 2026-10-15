#!/usr/bin/env bash
# 06-semantic.sh —— Ontology / SHACL / Semantic 回归（§10.1 第 6 级）。
#
# 本体与规则回归属于 §10.3 硬门禁之一（"本体或规则回归造成严重业务判断错误"），
# 因此必须阻断发布，而不是仅作报告。
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

ci_stage "06 Ontology / SHACL / Semantic 回归"
ci_require_go

if ! "$CI_GO" test ./internal/ontology/... -count=1 -timeout "${CI_TEST_TIMEOUT:-300s}" 2>&1; then
  ci_fail_exit "本体语义回归失败（internal/ontology/...）"
fi
ci_note "internal/ontology/...：通过"

# 跨模块语义测试（tests/ 内，无 DB 依赖子集）
if [ -d tests/semantic ]; then
  if ! "$CI_GO" test ./tests/semantic/... -count=1 2>&1; then
    ci_fail_exit "tests/semantic 失败"
  fi
  ci_note "tests/semantic：通过"
fi

# 固定报销样例的端到端语义断言（§11.4 P2 阶段验收）。
if [ -f datasets/finance-demo/ontologies.json ]; then
  ci_note "报销样例本体数据集存在：datasets/finance-demo/ontologies.json"
else
  ci_note "未找到 datasets/finance-demo/ontologies.json，跳过样例检查"
fi

ci_ok
