import { CheckCircle2, CircleDashed, FileCheck2, XCircle } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Badge } from "@/components/ui/badge";
import { formatDate } from "@/lib/format";
import type { TeamTaskData } from "@/types/team";
import { CollapsibleSection } from "./task-detail-content";
import { getAcceptanceReport, getTaskContract } from "./task-contract-utils";

function ItemList({ label, items }: { label: string; items?: string[] }) {
  if (!items || items.length === 0) return null;
  return (
    <div className="space-y-1">
      <p className="text-xs font-medium text-muted-foreground">{label}</p>
      <ul className="list-disc space-y-0.5 pl-5 text-sm">
        {items.map((item, i) => <li key={i} className="whitespace-pre-wrap break-words">{item}</li>)}
      </ul>
    </div>
  );
}

/** Trạng thái một tiêu chí: ký hiệu kèm chữ, không chỉ dựa vào màu. */
function CriterionStatus({ met }: { met: boolean | undefined }) {
  const { t } = useTranslation("teams");
  if (met === undefined) {
    return (
      <span className="inline-flex items-center gap-1 text-muted-foreground">
        <CircleDashed className="h-4 w-4 shrink-0" aria-hidden />{t("tasks.contract.criterionUnreported")}
      </span>
    );
  }
  return met ? (
    <span className="inline-flex items-center gap-1 text-green-700 dark:text-green-400">
      <CheckCircle2 className="h-4 w-4 shrink-0" aria-hidden />{t("tasks.contract.criterionMet")}
    </span>
  ) : (
    <span className="inline-flex items-center gap-1 text-red-700 dark:text-red-400">
      <XCircle className="h-4 w-4 shrink-0" aria-hidden />{t("tasks.contract.criterionUnmet")}
    </span>
  );
}

/**
 * Thẻ Task Contract trong chi tiết task: mục đích, sản phẩm, tiêu chí nghiệm
 * thu kèm báo cáo của member, phạm vi, giới hạn và các amendment. Task không
 * có contract thì không hiện gì.
 */
export function TaskDetailContract({ task }: { task: TeamTaskData }) {
  const { t } = useTranslation("teams");
  const contract = getTaskContract(task);
  if (!contract) return null;
  const report = getAcceptanceReport(task);
  const amendments = contract.amendments ?? [];
  const title = contract.version
    ? `${t("tasks.contract.title")} · ${t("tasks.contract.version", { version: contract.version })}`
    : t("tasks.contract.title");

  return (
    <CollapsibleSection icon={FileCheck2} title={title}>
      <div className="space-y-4">
        <div className="space-y-1">
          <p className="text-xs font-medium text-muted-foreground">{t("tasks.contract.objective")}</p>
          <p className="whitespace-pre-wrap break-words text-sm">{contract.objective}</p>
        </div>

        <ItemList label={t("tasks.contract.deliverables")} items={contract.deliverables} />

        {contract.acceptance.length > 0 && (
          <div className="space-y-1">
            <p className="text-xs font-medium text-muted-foreground">{t("tasks.contract.acceptance")}</p>
            <div className="overflow-x-auto overscroll-contain rounded-md border">
              <table className="w-full min-w-[600px] text-sm">
                <tbody>
                  {contract.acceptance.map((criterion, i) => {
                    const item = report.get(i + 1);
                    return (
                      <tr key={i} className="border-b last:border-b-0 align-top">
                        <td className="w-8 px-3 py-2 text-muted-foreground">{i + 1}.</td>
                        <td className="px-3 py-2 whitespace-pre-wrap break-words">{criterion}</td>
                        <td className="w-36 px-3 py-2"><CriterionStatus met={item?.met} /></td>
                        <td className="px-3 py-2 text-xs text-muted-foreground whitespace-pre-wrap break-words">
                          {item?.evidence ? (
                            <><span className="sr-only">{t("tasks.contract.evidence")}: </span>{item.evidence}</>
                          ) : null}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          </div>
        )}

        <ItemList label={t("tasks.contract.scopeIn")} items={contract.scope_in} />
        <ItemList label={t("tasks.contract.scopeOut")} items={contract.scope_out} />
        <ItemList label={t("tasks.contract.inputs")} items={contract.inputs} />
        <ItemList label={t("tasks.contract.needs")} items={contract.needs} />

        {!!contract.limits?.max_iterations && (
          <p className="text-sm text-muted-foreground">
            {t("tasks.contract.maxIterationsValue", { count: contract.limits.max_iterations })}
          </p>
        )}

        {amendments.length > 0 && (
          <div className="space-y-2">
            <p className="text-xs font-medium text-muted-foreground">{t("tasks.contract.amendments")}</p>
            <ol className="space-y-2">
              {amendments.map((a, i) => (
                <li key={`${a.version}-${i}`} className="rounded-md border bg-muted/30 p-2.5">
                  <div className="mb-1 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                    <Badge variant="outline" className="text-2xs">v{a.version}</Badge>
                    <span>{t(`tasks.contract.amendmentKind.${a.kind}`, { defaultValue: a.kind })}</span>
                    {a.by && <span>· {a.by}</span>}
                    {a.at && <span>· {formatDate(a.at)}</span>}
                  </div>
                  <p className="whitespace-pre-wrap break-words text-sm">{a.text}</p>
                </li>
              ))}
            </ol>
          </div>
        )}
      </div>
    </CollapsibleSection>
  );
}
