import { useTranslation } from "react-i18next";
import { AlertTriangle } from "lucide-react";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { ChartWrapper } from "@/pages/usage/components/chart-wrapper";
import type { MemoryEffectiveness } from "@/types/memory-effectiveness";
import { formatCount } from "./effectiveness-format";

/** Phễu truy hồi theo kênh, kèm cảnh báo khi auto-inject hoặc bộ chấm đang tắt. */
export function EffectivenessFunnel({ data }: { data: MemoryEffectiveness }) {
  const { t } = useTranslation("memory");
  const { switches } = data;

  return (
    <ChartWrapper title={t("effectiveness.funnel.title")} subtitle={t("effectiveness.funnel.subtitle")}>
      <div className="space-y-3">
        {!switches.auto_inject_enabled && (
          <Alert>
            <AlertTriangle />
            <AlertDescription>{t("effectiveness.funnel.autoInjectOff")}</AlertDescription>
          </Alert>
        )}
        {!switches.eval_enabled && (
          <Alert>
            <AlertTriangle />
            <AlertDescription>{t("effectiveness.funnel.evalOff")}</AlertDescription>
          </Alert>
        )}
        <div className="overflow-x-auto">
          <table className="w-full min-w-[600px] text-sm">
            <thead>
              <tr className="border-b text-left text-xs text-muted-foreground">
                <th className="py-2 pr-3 font-medium">{t("effectiveness.funnel.channel")}</th>
                <th className="py-2 pr-3 text-right font-medium">{t("effectiveness.funnel.eligible")}</th>
                <th className="py-2 pr-3 text-right font-medium">{t("effectiveness.funnel.matched")}</th>
                <th className="py-2 pr-3 text-right font-medium">{t("effectiveness.funnel.injected")}</th>
                <th className="py-2 text-right font-medium">{t("effectiveness.funnel.errors")}</th>
              </tr>
            </thead>
            <tbody>
              {data.funnel.map((f) => (
                <tr key={f.channel} className="border-b last:border-0">
                  <td className="py-2 pr-3">{t(`effectiveness.channelNames.${f.channel}`, { defaultValue: f.channel })}</td>
                  <td className="py-2 pr-3 text-right tabular-nums">{formatCount(f.eligible)}</td>
                  <td className="py-2 pr-3 text-right tabular-nums">{formatCount(f.matched)}</td>
                  <td className="py-2 pr-3 text-right tabular-nums">{formatCount(f.injected)}</td>
                  <td className="py-2 text-right tabular-nums">{formatCount(f.errors)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </ChartWrapper>
  );
}
