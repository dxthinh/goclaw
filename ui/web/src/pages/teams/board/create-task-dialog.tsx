import { useState } from "react";
import { Button } from "@/components/ui/button";
import {
  Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter,
} from "@/components/ui/dialog";
import { useTranslation } from "react-i18next";
import type { TeamTaskData, TeamMemberData, ScopeEntry } from "@/types/team";
import type { CreateTaskParams } from "../hooks/use-teams";
import { ContractFields } from "./contract-fields";
import { buildAssigneeOptions } from "./board-utils";
import {
  EMPTY_CONTRACT_FORM, buildContractInput, isContractRequired, validateContractForm,
  type ContractFormState,
} from "../task-sections/task-contract-utils";

interface CreateTaskDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  teamId: string;
  members: TeamMemberData[];
  selectedScope: ScopeEntry | null;
  /** Team bật task_contract (bản Standard): hiện khối contract trong form. */
  taskContractEnabled: boolean;
  createTask: (teamId: string, params: CreateTaskParams) => Promise<TeamTaskData>;
  onCreated: () => void;
}

const TASK_TYPES = ["general", "delegation", "escalation"] as const;

export function CreateTaskDialog({
  open, onOpenChange, teamId, members, selectedScope, taskContractEnabled, createTask, onCreated,
}: CreateTaskDialogProps) {
  const { t } = useTranslation("teams");
  const [subject, setSubject] = useState("");
  const [description, setDescription] = useState("");
  const [priority, setPriority] = useState(0);
  const [taskType, setTaskType] = useState<string>("general");
  const [assignTo, setAssignTo] = useState("");
  const [contract, setContract] = useState<ContractFormState>(EMPTY_CONTRACT_FORM);
  const [submitted, setSubmitted] = useState(false);
  const [creating, setCreating] = useState(false);
  // Lead không nhận được task: dispatch đánh task failed ngay sau khi tạo
  // (giống ô chọn của retry), nên chỉ liệt kê member.
  const assignees = buildAssigneeOptions(members);

  // Quy tắc giống backend: team bật contract, có assignee, loại task không miễn.
  const contractRequired = isContractRequired(taskContractEnabled, taskType, !!assignTo);
  const contractErrors = taskContractEnabled ? validateContractForm(contract, contractRequired) : [];

  const reset = () => {
    setSubject(""); setDescription(""); setPriority(0);
    setTaskType("general"); setAssignTo("");
    setContract(EMPTY_CONTRACT_FORM); setSubmitted(false);
  };

  const handleCreate = async () => {
    if (!subject.trim()) return;
    setSubmitted(true);
    if (contractErrors.length > 0) return;
    setCreating(true);
    try {
      await createTask(teamId, {
        subject: subject.trim(),
        description: description.trim() || undefined,
        priority,
        taskType: taskType || undefined,
        assignTo: assignTo || undefined,
        channel: selectedScope?.channel || undefined,
        chatId: selectedScope?.chat_id || undefined,
        contract: taskContractEnabled ? buildContractInput(contract) : undefined,
      });
      reset();
      onOpenChange(false);
      onCreated();
    } catch {
      // toast handled by hook; lỗi thiếu trường từ RPC hiện trong toast
    } finally {
      setCreating(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={(v) => { if (!creating) onOpenChange(v); }}>
      <DialogContent className="w-[95vw] sm:max-w-lg sm:max-h-[90vh] sm:overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{t("tasks.createTaskTitle")}</DialogTitle>
        </DialogHeader>
        <div className="space-y-4 py-2">
          <div className="space-y-1.5">
            <label htmlFor="task-subject" className="text-sm font-medium">{t("tasks.columns.subject")} *</label>
            <input id="task-subject" type="text" value={subject} onChange={(e) => setSubject(e.target.value)}
              placeholder={t("tasks.subjectPlaceholder")}
              className="w-full rounded-md border bg-background px-3 py-2 text-base md:text-sm" autoFocus />
          </div>
          <div className="space-y-1.5">
            <label htmlFor="task-description" className="text-sm font-medium">{t("tasks.detail.description")}</label>
            <textarea id="task-description" value={description} onChange={(e) => setDescription(e.target.value)}
              placeholder={t("tasks.descriptionPlaceholder")} rows={3}
              className="w-full resize-none rounded-md border bg-background px-3 py-2 text-base md:text-sm" />
          </div>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <div className="space-y-1.5">
              <label htmlFor="task-type" className="text-sm font-medium">{t("tasks.detail.type").replace(":", "")}</label>
              <select id="task-type" value={taskType} onChange={(e) => setTaskType(e.target.value)}
                className="w-full rounded-md border bg-background px-3 py-2 text-base md:text-sm">
                {TASK_TYPES.map((type) => (
                  <option key={type} value={type}>{t(`tasks.types.${type}`)}</option>
                ))}
              </select>
            </div>
            <div className="space-y-1.5">
              <label htmlFor="task-priority" className="text-sm font-medium">{t("tasks.priorityLabel")}</label>
              <input id="task-priority" type="number" value={priority} onChange={(e) => setPriority(Number(e.target.value))}
                min={0} className="w-full rounded-md border bg-background px-3 py-2 text-base md:text-sm" />
            </div>
          </div>
          {assignees.length > 0 && (
            <div className="space-y-1.5">
              <label htmlFor="task-assignee" className="text-sm font-medium">{t("tasks.assignTo")}</label>
              <select id="task-assignee" value={assignTo} onChange={(e) => setAssignTo(e.target.value)}
                className="w-full rounded-md border bg-background px-3 py-2 text-base md:text-sm">
                <option value="">{t("tasks.unassigned")}</option>
                {assignees.map((a) => (
                  <option key={a.id} value={a.id}>{a.name}</option>
                ))}
              </select>
            </div>
          )}
          {taskContractEnabled && (
            <ContractFields
              value={contract}
              onChange={setContract}
              required={contractRequired}
              errors={submitted ? contractErrors : []}
              disabled={creating}
            />
          )}
        </div>
        <DialogFooter className="gap-2">
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={creating}>
            {t("create.cancel")}
          </Button>
          <Button onClick={handleCreate} disabled={creating || !subject.trim() || (submitted && contractErrors.length > 0)}>
            {creating ? t("create.creating") : t("tasks.createTask")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
