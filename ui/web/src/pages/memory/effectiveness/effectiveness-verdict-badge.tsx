import { useTranslation } from "react-i18next";
import { CheckCircle2, CircleHelp, PowerOff, SlidersHorizontal, type LucideIcon } from "lucide-react";
import type { MemoryLayerVerdict } from "@/types/memory-effectiveness";

const STYLES: Record<MemoryLayerVerdict, { icon: LucideIcon; className: string }> = {
  keep: { icon: CheckCircle2, className: "bg-green-100 text-green-700 dark:bg-green-900 dark:text-green-300" },
  tune: { icon: SlidersHorizontal, className: "bg-amber-100 text-amber-700 dark:bg-amber-900 dark:text-amber-300" },
  consider_off: { icon: PowerOff, className: "bg-red-100 text-red-700 dark:bg-red-900 dark:text-red-300" },
  insufficient_data: { icon: CircleHelp, className: "bg-gray-100 text-gray-700 dark:bg-gray-800 dark:text-gray-300" },
};

/** Nhãn kết luận có cả biểu tượng và chữ, không chỉ dựa vào màu. */
export function VerdictBadge({ verdict }: { verdict: MemoryLayerVerdict }) {
  const { t } = useTranslation("memory");
  const style = STYLES[verdict] ?? STYLES.insufficient_data;
  const Icon = style.icon;
  return (
    <span className={`inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium ${style.className}`}>
      <Icon className="h-3 w-3" aria-hidden="true" />
      {t(`effectiveness.verdict.${verdict}`, { defaultValue: verdict })}
    </span>
  );
}
