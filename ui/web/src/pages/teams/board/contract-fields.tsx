import { useState } from "react";
import { ChevronDown, ChevronRight } from "lucide-react";
import { useTranslation } from "react-i18next";
import { CONTRACT_MAX_TOTAL, type ContractFormError, type ContractFormState } from "../task-sections/task-contract-utils";

interface ContractFieldsProps {
  value: ContractFormState;
  onChange: (next: ContractFormState) => void;
  /** Contract bắt buộc (team bật task_contract và task được giao ngay). */
  required: boolean;
  /** Lỗi chỉ hiện sau khi người dùng bấm tạo lần đầu. */
  errors: ContractFormError[];
  disabled?: boolean;
}

const MAIN_FIELDS = new Set<keyof ContractFormState>(["objective", "deliverables", "acceptance"]);

const INPUT_CLASS = "w-full rounded-md border bg-background px-3 py-2 text-base md:text-sm";

/**
 * Ô nhập Task Contract trong form tạo task: ba trường chính (mục đích, sản
 * phẩm, tiêu chí nghiệm thu) và phần tùy chọn thu gọn được. Trường danh sách
 * nhập mỗi dòng một mục.
 */
export function ContractFields({ value, onChange, required, errors, disabled }: ContractFieldsProps) {
  const { t } = useTranslation("teams");
  const [moreOpenState, setMoreOpen] = useState(false);
  // Lỗi ở trường tùy chọn thì mở sẵn phần mở rộng để người dùng thấy lỗi.
  const moreOpen = moreOpenState || errors.some((e) => e.field !== "total" && !MAIN_FIELDS.has(e.field));
  const totalError = errors.find((e) => e.code === "totalTooLong");
  const set = <K extends keyof ContractFormState>(key: K, v: ContractFormState[K]) => onChange({ ...value, [key]: v });

  const errorFor = (field: keyof ContractFormState) => {
    const e = errors.find((x) => x.field === field);
    return e ? t(`tasks.contract.errors.${e.code}`) : undefined;
  };

  const textArea = (field: keyof ContractFormState, label: string, opts: { placeholder?: string; hint?: string; mark?: boolean; rows?: number } = {}) => {
    const err = errorFor(field);
    const id = `contract-${field}`;
    return (
      <div className="space-y-1.5">
        <label htmlFor={id} className="text-sm font-medium">{label}{opts.mark && required ? " *" : ""}</label>
        <textarea
          id={id}
          value={value[field] as string}
          onChange={(e) => set(field, e.target.value as never)}
          placeholder={opts.placeholder}
          rows={opts.rows ?? 2}
          disabled={disabled}
          aria-invalid={!!err}
          className={`${INPUT_CLASS} resize-y ${err ? "border-destructive" : ""}`}
        />
        {opts.hint && <p className="text-xs text-muted-foreground">{opts.hint}</p>}
        {err && <p className="text-xs text-destructive">{err}</p>}
      </div>
    );
  };

  return (
    <fieldset className="space-y-3 rounded-lg border p-3" disabled={disabled}>
      <legend className="px-1 text-sm font-semibold">{t("tasks.contract.title")}</legend>
      <p className="text-xs text-muted-foreground">
        {required ? t("tasks.contract.required") : t("tasks.contract.optionalHint")}
      </p>
      {totalError && totalError.code === "totalTooLong" && (
        <p className="text-xs text-destructive" role="alert">
          {t("tasks.contract.errors.totalTooLong", { max: CONTRACT_MAX_TOTAL, count: totalError.count })}
        </p>
      )}

      {textArea("objective", t("tasks.contract.objective"), { placeholder: t("tasks.contract.objectivePlaceholder"), mark: true })}
      {textArea("deliverables", t("tasks.contract.deliverables"), { placeholder: t("tasks.contract.deliverablesPlaceholder"), mark: true, rows: 3 })}
      {textArea("acceptance", t("tasks.contract.acceptance"), { placeholder: t("tasks.contract.acceptancePlaceholder"), mark: true, rows: 3 })}

      <button
        type="button"
        onClick={() => setMoreOpen((v) => !v)}
        className="flex min-h-[44px] w-full items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground"
        aria-expanded={moreOpen}
      >
        {moreOpen ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
        {t("tasks.contract.more")}
      </button>

      {moreOpen && (
        <div className="space-y-3">
          {textArea("scopeIn", t("tasks.contract.scopeIn"), { hint: t("tasks.contract.linesHint") })}
          {textArea("scopeOut", t("tasks.contract.scopeOut"), { hint: t("tasks.contract.linesHint") })}
          {textArea("inputs", t("tasks.contract.inputs"), { hint: t("tasks.contract.linesHint") })}
          {textArea("needs", t("tasks.contract.needs"), { hint: t("tasks.contract.needsHint") })}
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <div className="space-y-1.5">
              <label htmlFor="contract-onUnclear" className="text-sm font-medium">{t("tasks.contract.onUnclear")}</label>
              <select
                id="contract-onUnclear"
                value={value.onUnclear}
                onChange={(e) => set("onUnclear", e.target.value as ContractFormState["onUnclear"])}
                className={INPUT_CLASS}
              >
                <option value="ask">{t("tasks.contract.onUnclearAsk")}</option>
                <option value="assume_and_note">{t("tasks.contract.onUnclearAssume")}</option>
              </select>
            </div>
            <div className="space-y-1.5">
              <label htmlFor="contract-maxIterations" className="text-sm font-medium">{t("tasks.contract.maxIterations")}</label>
              <input
                id="contract-maxIterations"
                type="number"
                inputMode="numeric"
                min={1}
                value={value.maxIterations}
                onChange={(e) => set("maxIterations", e.target.value)}
                aria-invalid={!!errorFor("maxIterations")}
                className={INPUT_CLASS}
              />
              <p className="text-xs text-muted-foreground">{t("tasks.contract.maxIterationsHint")}</p>
              {errorFor("maxIterations") && <p className="text-xs text-destructive">{errorFor("maxIterations")}</p>}
            </div>
          </div>
        </div>
      )}
    </fieldset>
  );
}
