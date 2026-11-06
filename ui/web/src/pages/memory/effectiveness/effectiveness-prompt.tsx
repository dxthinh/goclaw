import { useTranslation } from "react-i18next";
import { ChartWrapper } from "@/pages/usage/components/chart-wrapper";
import type { MemoryEffectiveness } from "@/types/memory-effectiveness";
import { formatCount } from "./effectiveness-format";

type PromptPart = "system" | "tool_schema" | "history" | "tool_result" | "memory";

const PARTS: { key: PromptPart; color: string }[] = [
  { key: "system", color: "bg-blue-500" },
  { key: "tool_schema", color: "bg-violet-500" },
  { key: "history", color: "bg-emerald-500" },
  { key: "tool_result", color: "bg-amber-500" },
  { key: "memory", color: "bg-rose-500" },
];

/** Thanh tỷ trọng token trung bình mỗi run, kèm số tuyệt đối. */
export function EffectivenessPrompt({ data }: { data: MemoryEffectiveness }) {
  const { t } = useTranslation("memory");
  const { runs, avg } = data.prompt;
  const total = PARTS.reduce((sum, p) => sum + avg[p.key], 0);

  return (
    <ChartWrapper
      title={t("effectiveness.prompt.title")}
      subtitle={t("effectiveness.prompt.subtitle", { runs: formatCount(runs) })}
      empty={runs === 0 || total === 0}
      emptyText={t("effectiveness.noValue")}
      height={80}
    >
      <div
        className="flex h-4 w-full overflow-hidden rounded-full bg-muted"
        role="img"
        aria-label={t("effectiveness.prompt.title")}
      >
        {PARTS.map((p) => (
          <div key={p.key} className={p.color} style={{ width: `${(avg[p.key] / total) * 100}%` }} />
        ))}
      </div>
      <ul className="mt-3 grid grid-cols-1 gap-2 text-sm sm:grid-cols-2 lg:grid-cols-5">
        {PARTS.map((p) => (
          <li key={p.key} className="flex items-center gap-2">
            <span className={`h-2.5 w-2.5 shrink-0 rounded-sm ${p.color}`} aria-hidden="true" />
            <span className="text-muted-foreground">{t(`effectiveness.prompt.parts.${p.key}`)}</span>
            <span className="ml-auto tabular-nums">
              {formatCount(avg[p.key])} ({((avg[p.key] / total) * 100).toFixed(1)}%)
            </span>
          </li>
        ))}
      </ul>
    </ChartWrapper>
  );
}
