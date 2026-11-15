import { useState, useEffect } from "react";
import {
  Dialog, DialogContent, DialogHeader, DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { useTranslation } from "react-i18next";

/* ── Action dialog (cancel with optional reason / retry with required comment) ── */

export interface TaskAssigneeOption { id: string; name: string }

interface TaskActionDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description: string;
  placeholder: string;
  confirmLabel: string;
  required?: boolean;
  requiredHint?: string;
  variant?: "default" | "destructive";
  loading?: boolean;
  /** When set, an assignee select is shown above the textarea (retry). */
  assignees?: TaskAssigneeOption[];
  assigneeLabel?: string;
  defaultAssigneeId?: string;
  onConfirm: (text: string, assigneeId?: string) => void;
}

export function TaskActionDialog({
  open, onOpenChange, title, description, placeholder, confirmLabel,
  required, requiredHint, variant = "default", loading,
  assignees, assigneeLabel, defaultAssigneeId, onConfirm,
}: TaskActionDialogProps) {
  const { t } = useTranslation("common");
  const [text, setText] = useState("");
  const [assigneeId, setAssigneeId] = useState<string>("");
  useEffect(() => {
    if (open) { setText(""); setAssigneeId(defaultAssigneeId ?? ""); }
  }, [open, defaultAssigneeId]);
  const withAssignee = !!assignees && assignees.length > 0;
  const missing = (!!required && text.trim() === "") || (withAssignee && assigneeId === "");

  return (
    <Dialog open={open} onOpenChange={(v) => { if (!loading) onOpenChange(v); }}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
        </DialogHeader>
        <p className="text-sm text-muted-foreground">{description}</p>
        {withAssignee && (
          <div className="space-y-1">
            {assigneeLabel && <p className="text-xs text-muted-foreground">{assigneeLabel}</p>}
            <Select value={assigneeId} onValueChange={setAssigneeId} disabled={loading}>
              <SelectTrigger className="h-9">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {assignees!.map((a) => (
                  <SelectItem key={a.id} value={a.id}>{a.name}</SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        )}
        <Textarea
          value={text}
          onChange={(e) => setText(e.target.value)}
          placeholder={placeholder}
          rows={4}
          autoFocus
          disabled={loading}
        />
        {missing && requiredHint && (
          <p className="text-xs text-muted-foreground">{requiredHint}</p>
        )}
        <div className="flex justify-end gap-2">
          <Button variant="outline" size="sm" onClick={() => onOpenChange(false)} disabled={loading}>
            {t("cancel")}
          </Button>
          <Button variant={variant} size="sm" onClick={() => onConfirm(text.trim(), assigneeId || undefined)} disabled={loading || missing}>
            {loading ? "..." : confirmLabel}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
