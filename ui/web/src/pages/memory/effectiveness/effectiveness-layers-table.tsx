import { useTranslation } from "react-i18next";
import { ChartWrapper } from "@/pages/usage/components/chart-wrapper";
import type { MemoryEffectiveness } from "@/types/memory-effectiveness";
import { formatCount, formatPercent } from "./effectiveness-format";
import { VerdictBadge } from "./effectiveness-verdict-badge";

/** Bảng kết luận theo tầng và ngưỡng đang áp dụng. */
export function EffectivenessLayersTable({ data }: { data: MemoryEffectiveness }) {
  const { t } = useTranslation("memory");
  const noData = t("effectiveness.noValue");
  const th = data.thresholds;
  const pct = (v: number) => `${Math.round(v * 100)}%`;

  return (
    <ChartWrapper title={t("effectiveness.layers.title")} subtitle={t("effectiveness.layers.subtitle")}>
      <div className="overflow-x-auto">
        <table className="w-full min-w-[600px] text-sm">
          <thead>
            <tr className="border-b text-left text-xs text-muted-foreground">
              <th className="py-2 pr-3 font-medium">{t("effectiveness.layers.layer")}</th>
              <th className="py-2 pr-3 text-right font-medium">{t("effectiveness.layers.samples")}</th>
              <th className="py-2 pr-3 text-right font-medium">{t("effectiveness.layers.relevance")}</th>
              <th className="py-2 pr-3 text-right font-medium">{t("effectiveness.layers.used")}</th>
              <th className="py-2 font-medium">{t("effectiveness.layers.verdict")}</th>
            </tr>
          </thead>
          <tbody>
            {data.layers.map((l) => (
              <tr key={l.layer} className="border-b last:border-0">
                <td className="py-2 pr-3">{t(`effectiveness.layerNames.${l.layer}`, { defaultValue: l.layer })}</td>
                <td className="py-2 pr-3 text-right tabular-nums">{formatCount(l.samples)}</td>
                <td className="py-2 pr-3 text-right tabular-nums">{formatPercent(l.relevance) ?? noData}</td>
                <td className="py-2 pr-3 text-right tabular-nums">{formatPercent(l.used) ?? noData}</td>
                <td className="py-2"><VerdictBadge verdict={l.verdict} /></td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <p className="mt-3 text-xs text-muted-foreground">
        {t("effectiveness.layers.thresholds", {
          minSamples: th.min_samples,
          keepRelevance: pct(th.keep_relevance),
          keepUsed: pct(th.keep_used),
          offRelevance: pct(th.off_relevance),
        })}
      </p>
    </ChartWrapper>
  );
}
