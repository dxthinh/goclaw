import { useState } from "react";
import { useTranslation } from "react-i18next";
import { BarChart3, RefreshCw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import type { MemoryEffectiveness, MemoryEffectivenessDays } from "@/types/memory-effectiveness";
import { useMemoryEffectiveness } from "../hooks/use-memory-effectiveness";
import { EffectivenessLayersTable } from "./effectiveness-layers-table";
import { EffectivenessFunnel } from "./effectiveness-funnel";
import { EffectivenessPrompt } from "./effectiveness-prompt";
import { EffectivenessCache } from "./effectiveness-cache";
import { EffectivenessPipeline } from "./effectiveness-pipeline";

const DAY_OPTIONS: MemoryEffectivenessDays[] = [7, 30, 90];

/** Rỗng khi chưa có bất kỳ số liệu nào trong khoảng, để phân biệt với "có dữ liệu nhưng bằng 0". */
function hasNoData(d: MemoryEffectiveness): boolean {
  return (
    d.funnel.every((f) => f.eligible === 0 && f.errors === 0 && f.disabled === 0) &&
    d.layers.every((l) => l.samples === 0) &&
    d.prompt.runs === 0 &&
    d.cache.length === 0 &&
    d.pipeline.background.length === 0 &&
    d.pipeline.episodic_total === 0 &&
    d.pipeline.kg_entities === 0 &&
    d.pipeline.memory_documents === 0
  );
}

/** agentId rỗng nghĩa là tất cả agent (bộ lọc agent dùng chung của trang Memory). */
export function EffectivenessTab({ agentId }: { agentId: string }) {
  const { t } = useTranslation("memory");
  const [days, setDays] = useState<MemoryEffectivenessDays>(7);
  const { data, loading, error, fetching, refetch } = useMemoryEffectiveness(agentId, days);

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-end gap-3">
        <div className="grid gap-1.5">
          <Label htmlFor="mem-eff-days" className="text-xs">{t("effectiveness.period")}</Label>
          <select
            id="mem-eff-days"
            value={days}
            onChange={(e) => setDays(Number(e.target.value) as MemoryEffectivenessDays)}
            className="h-9 rounded-md border bg-background px-3 text-base md:text-sm cursor-pointer"
          >
            {DAY_OPTIONS.map((d) => (
              <option key={d} value={d}>{t("effectiveness.days", { count: d })}</option>
            ))}
          </select>
        </div>
        <Button
          variant="outline"
          size="icon"
          className="h-9 w-9"
          onClick={() => refetch()}
          disabled={fetching}
          aria-label={t("effectiveness.refresh")}
        >
          <RefreshCw className={"h-3.5 w-3.5" + (fetching ? " animate-spin" : "")} />
        </Button>
        {data && (
          <p className="pb-2 text-xs text-muted-foreground">
            {t("effectiveness.window", { from: data.window.from, to: data.window.to })}
          </p>
        )}
      </div>

      {loading ? (
        <div className="space-y-4">
          <Skeleton className="h-[220px] w-full" />
          <Skeleton className="h-[160px] w-full" />
        </div>
      ) : error || !data ? (
        <div className="flex flex-col items-center gap-3 rounded-lg border py-10 text-center">
          <p className="text-sm text-destructive">{t("effectiveness.error")}</p>
          <Button variant="outline" size="sm" onClick={() => refetch()}>{t("effectiveness.retry")}</Button>
        </div>
      ) : hasNoData(data) ? (
        <div className="flex flex-col items-center justify-center gap-2 rounded-lg border py-12 text-center">
          <BarChart3 className="h-8 w-8 text-muted-foreground/40" />
          <p className="text-sm font-medium">{t("effectiveness.empty.title")}</p>
          <p className="max-w-md text-xs text-muted-foreground">{t("effectiveness.empty.hint")}</p>
        </div>
      ) : (
        <>
          <EffectivenessLayersTable data={data} />
          <EffectivenessFunnel data={data} />
          <EffectivenessPrompt data={data} />
          <EffectivenessCache data={data} />
          <EffectivenessPipeline data={data} />
          <p className="text-xs text-muted-foreground">{t("effectiveness.rollupNote")}</p>
        </>
      )}
    </div>
  );
}
