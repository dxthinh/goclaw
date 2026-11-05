import { useTranslation } from "react-i18next";
import { ChartWrapper } from "@/pages/usage/components/chart-wrapper";
import type { MemoryEffectiveness } from "@/types/memory-effectiveness";
import { formatCount, formatPercent } from "./effectiveness-format";

/** Tỷ lệ cache theo provider/model; null hiện chữ không có dữ liệu chứ không hiện 0%. */
export function EffectivenessCache({ data }: { data: MemoryEffectiveness }) {
  const { t } = useTranslation("memory");
  return (
    <ChartWrapper
      title={t("effectiveness.cache.title")}
      subtitle={t("effectiveness.cache.subtitle")}
      empty={data.cache.length === 0}
      emptyText={t("effectiveness.noValue")}
      height={80}
    >
      <div className="overflow-x-auto">
        <table className="w-full min-w-[600px] text-sm">
          <thead>
            <tr className="border-b text-left text-xs text-muted-foreground">
              <th className="py-2 pr-3 font-medium">{t("effectiveness.cache.provider")}</th>
              <th className="py-2 pr-3 font-medium">{t("effectiveness.cache.model")}</th>
              <th className="py-2 pr-3 text-right font-medium">{t("effectiveness.cache.calls")}</th>
              <th className="py-2 text-right font-medium">{t("effectiveness.cache.rate")}</th>
            </tr>
          </thead>
          <tbody>
            {data.cache.map((c) => (
              <tr key={`${c.provider}/${c.model}`} className="border-b last:border-0">
                <td className="py-2 pr-3">{c.provider}</td>
                <td className="py-2 pr-3 break-all">{c.model}</td>
                <td className="py-2 pr-3 text-right tabular-nums">{formatCount(c.calls)}</td>
                <td className="py-2 text-right tabular-nums">
                  {formatPercent(c.cache_rate) ?? t("effectiveness.noValue")}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </ChartWrapper>
  );
}
