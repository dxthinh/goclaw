import { useTranslation } from "react-i18next";
import { ChartWrapper } from "@/pages/usage/components/chart-wrapper";
import type { MemoryEffectiveness } from "@/types/memory-effectiveness";
import { formatCount } from "./effectiveness-format";

const COUNTERS = [
  "episodic_total",
  "episodic_stale",
  "episodic_promoted",
  "kg_entities",
  "kg_relations",
  "memory_documents",
  "dreaming_documents",
] as const;

/** Số lượng pipeline nền cuối khoảng và token LLM nền theo mục đích. */
export function EffectivenessPipeline({ data }: { data: MemoryEffectiveness }) {
  const { t } = useTranslation("memory");
  const { pipeline } = data;
  return (
    <ChartWrapper title={t("effectiveness.pipeline.title")} subtitle={t("effectiveness.pipeline.subtitle")}>
      <dl className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
        {COUNTERS.map((key) => (
          <div key={key} className="rounded-md border p-3">
            <dt className="text-xs text-muted-foreground">{t(`effectiveness.pipeline.counters.${key}`)}</dt>
            <dd className="mt-1 text-lg font-semibold tabular-nums">{formatCount(pipeline[key])}</dd>
          </div>
        ))}
      </dl>

      <h4 className="mt-4 text-xs font-semibold">{t("effectiveness.pipeline.background")}</h4>
      {pipeline.background.length === 0 ? (
        <p className="mt-2 text-xs text-muted-foreground">{t("effectiveness.noValue")}</p>
      ) : (
        <div className="mt-2 overflow-x-auto">
          <table className="w-full min-w-[600px] text-sm">
            <thead>
              <tr className="border-b text-left text-xs text-muted-foreground">
                <th className="py-2 pr-3 font-medium">{t("effectiveness.pipeline.purpose")}</th>
                <th className="py-2 pr-3 text-right font-medium">{t("effectiveness.pipeline.calls")}</th>
                <th className="py-2 pr-3 text-right font-medium">{t("effectiveness.pipeline.inputTokens")}</th>
                <th className="py-2 text-right font-medium">{t("effectiveness.pipeline.outputTokens")}</th>
              </tr>
            </thead>
            <tbody>
              {pipeline.background.map((b) => (
                <tr key={b.purpose} className="border-b last:border-0">
                  <td className="py-2 pr-3">{b.purpose}</td>
                  <td className="py-2 pr-3 text-right tabular-nums">{formatCount(b.calls)}</td>
                  <td className="py-2 pr-3 text-right tabular-nums">{formatCount(b.input_tokens)}</td>
                  <td className="py-2 text-right tabular-nums">{formatCount(b.output_tokens)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </ChartWrapper>
  );
}
