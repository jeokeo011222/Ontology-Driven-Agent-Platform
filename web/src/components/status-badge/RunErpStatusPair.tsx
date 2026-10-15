import { Link } from 'react-router-dom';
import { RunStatusBadge, ErpOperationStatusBadge } from './StatusBadges';
import { isBusinessConfirmedSuccess, needsReconciliation } from './status-maps';
import type { ErpOperationStatus, RunStatus } from '@/types';
import { Icon } from '@/components/icon';

export interface RunErpStatusPairProps {
  runStatus: RunStatus;
  /** 该 Run 关联的 ERP 操作状态；未涉及外部操作时传 null/undefined */
  erpStatus?: ErpOperationStatus | null;
  erpOperationId?: string;
  reconciliationId?: string;
  /** 对账入口链接（§4.3 / §13.2：结果待确认时必须提供对账入口） */
  reconcileHref?: string;
}

/**
 * Run 状态与 ERP Operation 状态并列展示（contracts/01 §5.1）。
 *
 * 为什么必须并列：
 *   Run = COMPLETED 而 ERP Operation = UNKNOWN 是**合法组合**。
 *   任何 UI 都不得把 Run 终态翻译成“业务已成功”。
 *   业务侧“已确认成功”的唯一判据是 ERP Operation = SUCCEEDED。
 */
export function RunErpStatusPair({
  runStatus,
  erpStatus,
  erpOperationId,
  reconciliationId,
  reconcileHref,
}: RunErpStatusPairProps) {
  const runCompleted = runStatus === 'COMPLETED';
  const businessConfirmed = isBusinessConfirmedSuccess(erpStatus);
  const reconcile = needsReconciliation(runStatus, erpStatus);

  // Run 已完成但业务未确认 —— 必须在界面上显式说明，避免误读为业务成功
  const misleadingSuccess = runCompleted && !businessConfirmed;

  return (
    <div className="stack stack--tight">
      <div className="status-pair">
        <div className="status-pair__block">
          <div className="status-pair__label">任务（Run）状态</div>
          <RunStatusBadge status={runStatus} />
          {runCompleted && erpStatus ? (
            <p className="status-pair__note">
              任务执行流程已结束；这不等于业务操作已成功。
            </p>
          ) : null}
        </div>
        <div className="status-pair__block">
          <div className="status-pair__label">外部操作（ERP Operation）状态</div>
          {erpStatus ? (
            <ErpOperationStatusBadge status={erpStatus} />
          ) : (
            <span className="muted text-sm">本任务未涉及外部写操作</span>
          )}
          {erpOperationId ? (
            <p className="text-xs muted" style={{ marginTop: 8 }}>
              操作 ID：<span className="copy-id">{erpOperationId}</span>
            </p>
          ) : null}
        </div>
      </div>

      {misleadingSuccess ? (
        <div className="banner banner--warning">
          <span className="banner__icon" aria-hidden="true">
            <Icon name="alertTriangle" size={16} />
          </span>
          <span>
            任务（Run）状态为“已完成”，但外部操作状态为
            {erpStatus ? `「${erpStatus}」` : '“未涉及”'}，业务结果尚未确认。请以外部操作状态为准。
          </span>
        </div>
      ) : null}

      {reconcile ? (
        <div className="banner banner--warning">
          <span className="banner__icon" aria-hidden="true">
            <Icon name="arrowLeftRight" size={16} />
          </span>
          <span>
            结果待确认：平台尚未取得可信的外部操作结果。
            {reconciliationId ? (
              <>
                {' '}
                对账编号 <span className="copy-id">{reconciliationId}</span>。
              </>
            ) : null}{' '}
            请通过对账确认最终状态，切勿按“失败”处理，也不要重复提交非幂等操作。
          </span>
          {reconcileHref ? (
            <Link className="btn btn--secondary btn--sm" to={reconcileHref} style={{ marginLeft: 'auto' }}>
              前往对账
            </Link>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
