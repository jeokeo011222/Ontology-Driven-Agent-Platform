# 部署脚本目录

## 定位

存放超出 `docker compose` 能力的部署与运维脚本（第十六篇 §4.1 目录规划）。

## 应放什么

适合放在这里：

- 多环境（dev / staging / prod）的编排脚本，含变量校验与前置检查；
- 发布流程脚本：拉取候选制品 → 校验 sha256（对照 `artifacts/ci/manifest.json`）→ 滚动更新 → 冒烟；
- 回滚脚本：切回上一版本制品并验证 `/readyz`；
- 备份与恢复脚本（见 `docs/runbooks/README.md` §4）。

不应放什么：

- 已被 Makefile 覆盖的日常目标（用 `make` 而非新脚本，避免两处真源）；
- 一次性排查命令（写进 `docs/runbooks/`，而不是沉淀成脚本）。

## 当前状态

**目录已建立，脚本尚未落盘。**

当前可直接使用的入口：

| 需求 | 入口 |
| --- | --- |
| 启停 | `docker compose up/down`，或仓库根的 `start.sh` / `stop.sh` / `restart.sh` |
| 迁移 | `make migrate` / `make migrate-mockerp` |
| 冒烟 | `bash tools/ci/11-smoke.sh` |
| 制品构建与校验 | `bash tools/ci/10-artifact.sh`（产出 `artifacts/ci/manifest.json`） |
| 本地流水线 | `bash tools/ci/run-all.sh` |
