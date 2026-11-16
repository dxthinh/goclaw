import { useState } from "react";
import { CheckCircle2, MessageCircleQuestion, PencilLine, ShieldCheck } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import type { TeamTaskComment, TeamTaskData } from "@/types/team";
import { TaskActionDialog, type TaskAssigneeOption } from "./task-action-dialog";
import { getCheckpointPending, getLatestClarifyQuestion } from "./task-contract-utils";

/** RPC phản hồi của dashboard; chỉ truyền ở bản Standard. */
export interface TaskFeedbackActions {
  answerTask: (teamId: string, taskId: string, text: string, assignee?: string) => Promise<void>;
  requestTaskChanges: (teamId: string, taskId: string, reason: string) => Promise<void>;
  approveTask: (teamId: string, taskId: string, comment?: string) => Promise<void>;
}

interface TaskDetailFeedbackProps {
  task: TeamTaskData;
  teamId: string;
  comments: TeamTaskComment[];
  actions: TaskFeedbackActions;
  assignees?: TaskAssigneeOption[];
  resolveMember: (id?: string) => string | undefined;
  /** Gọi sau khi thao tác thành công để tải lại chi tiết task. */
  onDone: () => void;
}

const TOUCH = "max-sm:min-h-11";

/**
 * Khối hành động theo trạng thái task:
 * - awaiting_input: câu hỏi mới nhất của member và ô trả lời (teams.tasks.answer).
 * - in_review có checkpoint_pending user/lead: duyệt kết quả (teams.tasks.approve)
 *   và yêu cầu sửa bắt buộc lý do (teams.tasks.request-changes).
 * Trạng thái khác không hiện gì.
 */
export function TaskDetailFeedback({ task, teamId, comments, actions, assignees, resolveMember, onDone }: TaskDetailFeedbackProps) {
  const { t } = useTranslation("teams");
  const [answer, setAnswer] = useState("");
  const [assignee, setAssignee] = useState("");
  const [busy, setBusy] = useState(false);
  const [changesOpen, setChangesOpen] = useState(false);

  const pending = getCheckpointPending(task);
  const awaiting = task.status === "awaiting_input";
  const reviewing = task.status === "in_review" && pending !== null;
  if (!awaiting && !reviewing) return null;

  const run = async (fn: () => Promise<void>) => {
    setBusy(true);
    try { await fn(); onDone(); return true; }
    catch { return false; /* toast handled by hook */ }
    finally { setBusy(false); }
  };

  if (awaiting) {
    const question = getLatestClarifyQuestion(comments);
    const agent = resolveMember(question?.agentId) || resolveMember(task.owner_agent_id) || task.owner_agent_key || "";
    const others = (assignees ?? []).filter((a) => a.id !== task.owner_agent_id);
    const send = async () => {
      const text = answer.trim();
      if (!text) return;
      if (await run(() => actions.answerTask(teamId, task.id, text, assignee || undefined))) {
        setAnswer(""); setAssignee("");
      }
    };
    return (
      <section className="space-y-3 rounded-lg border border-violet-500/40 bg-violet-500/5 p-3 sm:p-4" aria-live="polite">
        <h3 className="flex items-center gap-2 text-sm font-semibold">
          <MessageCircleQuestion className="h-4 w-4 shrink-0" aria-hidden />
          {t("tasks.feedback.awaitingTitle")}
        </h3>
        {question ? (
          <div className="space-y-1">
            <p className="text-xs text-muted-foreground">
              {question.kind === "missing_tool"
                ? t("tasks.feedback.missingTool", { agent })
                : t("tasks.feedback.question", { agent })}
            </p>
            <p className="whitespace-pre-wrap break-words text-sm">{question.text}</p>
          </div>
        ) : (
          <p className="text-sm text-muted-foreground">{t("tasks.feedback.noQuestion")}</p>
        )}
        <Textarea
          value={answer}
          onChange={(e) => setAnswer(e.target.value)}
          placeholder={t("tasks.feedback.answerPlaceholder")}
          rows={3}
          disabled={busy}
        />
        <div className="flex flex-col gap-2 sm:flex-row sm:items-end">
          {others.length > 0 && (
            <div className="flex-1 space-y-1">
              <label htmlFor="answer-assignee" className="text-xs text-muted-foreground">{t("tasks.feedback.answerAssignee")}</label>
              <select
                id="answer-assignee"
                value={assignee}
                onChange={(e) => setAssignee(e.target.value)}
                disabled={busy}
                className="w-full rounded-md border bg-background px-3 py-2 text-base md:text-sm"
              >
                <option value="">{t("tasks.feedback.keepOwner")}</option>
                {others.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
              </select>
            </div>
          )}
          <Button onClick={send} disabled={busy || !answer.trim()} className={`${TOUCH} sm:ml-auto`}>
            {busy ? "..." : t("tasks.feedback.answer")}
          </Button>
        </div>
      </section>
    );
  }

  const approve = () => run(() => actions.approveTask(teamId, task.id));
  const requestChanges = async (reason: string) => {
    if (!reason) return;
    if (await run(() => actions.requestTaskChanges(teamId, task.id, reason))) setChangesOpen(false);
  };

  return (
    <section className="space-y-3 rounded-lg border border-cyan-500/40 bg-cyan-500/5 p-3 sm:p-4" aria-live="polite">
      <h3 className="flex items-center gap-2 text-sm font-semibold">
        <ShieldCheck className="h-4 w-4 shrink-0" aria-hidden />
        {pending === "user" ? t("tasks.feedback.reviewTitleUser") : t("tasks.feedback.reviewTitleLead")}
      </h3>
      <p className="text-sm text-muted-foreground">
        {pending === "user"
          ? t("tasks.feedback.reviewHintUser", { number: task.task_number ?? "" })
          : t("tasks.feedback.reviewHintLead")}
      </p>
      <div className="flex flex-col gap-2 sm:flex-row sm:justify-end">
        <Button variant="outline" onClick={() => setChangesOpen(true)} disabled={busy} className={TOUCH}>
          <PencilLine className="mr-1.5 h-3.5 w-3.5" />
          {t("tasks.feedback.requestChanges")}
        </Button>
        <Button onClick={approve} disabled={busy} className={TOUCH}>
          <CheckCircle2 className="mr-1.5 h-3.5 w-3.5" />
          {busy ? "..." : t("tasks.feedback.approve")}
        </Button>
      </div>
      <TaskActionDialog
        open={changesOpen}
        onOpenChange={setChangesOpen}
        title={t("tasks.feedback.requestChangesTitle")}
        description={t("tasks.feedback.requestChangesDescription")}
        placeholder={t("tasks.feedback.requestChangesPlaceholder")}
        confirmLabel={t("tasks.feedback.requestChanges")}
        required
        requiredHint={t("tasks.feedback.requestChangesRequired")}
        loading={busy}
        onConfirm={(text) => { void requestChanges(text); }}
      />
    </section>
  );
}
