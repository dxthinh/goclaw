import { useState, useCallback, useMemo, useRef } from "react";
import { useTranslation } from "react-i18next";
import {
  DndContext,
  closestCenter,
  KeyboardSensor,
  PointerSensor,
  useSensor,
  useSensors,
  type DragEndEvent,
} from "@dnd-kit/core";
import {
  SortableContext,
  verticalListSortingStrategy,
  arrayMove,
  sortableKeyboardCoordinates,
} from "@dnd-kit/sortable";
import { Plus, Loader2 } from "lucide-react";
import { uniqueId } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { DialogHeader, DialogTitle, DialogDescription, DialogFooter } from "@/components/ui/dialog";
import { useProviders } from "@/pages/providers/hooks/use-providers";
import { SortableProviderCard } from "./media-sortable-provider-card";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import type { ImageInputMode, ProviderEntry } from "./media-provider-chain-helpers";
import {
  buildChainSettings,
  formatToolTitle,
  parseImageInputMode,
  parseInitialEntries,
} from "./media-provider-chain-helpers";

interface MediaProviderChainFormProps {
  toolName: string;
  initialSettings: Record<string, unknown>;
  onSave: (settings: Record<string, unknown>) => Promise<void>;
  onCancel: () => void;
}

/** Dialog form for configuring an ordered provider chain for a media tool. */
export function MediaProviderChainForm({
  toolName,
  initialSettings,
  onSave,
  onCancel,
}: MediaProviderChainFormProps) {
  const { t } = useTranslation("tools");
  const { providers } = useProviders();
  const enabledProviders = useMemo(() => providers.filter((p) => p.enabled), [providers]);
  const portalRef = useRef<HTMLDivElement | null>(null);

  const [entries, setEntries] = useState<ProviderEntry[]>(() =>
    parseInitialEntries(initialSettings, providers),
  );
  const [imageInput, setImageInput] = useState<ImageInputMode>(() => parseImageInputMode(initialSettings));
  const [saving, setSaving] = useState(false);
  const isReadImage = toolName === "read_image";

  const sensors = useSensors(
    useSensor(PointerSensor),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );

  const handleDragEnd = useCallback((event: DragEndEvent) => {
    const { active, over } = event;
    if (over && active.id !== over.id) {
      setEntries((prev) => {
        const oldIndex = prev.findIndex((e) => e.id === active.id);
        const newIndex = prev.findIndex((e) => e.id === over.id);
        return arrayMove(prev, oldIndex, newIndex);
      });
    }
  }, []);

  const handleUpdate = useCallback((id: string, patch: Partial<ProviderEntry>) => {
    setEntries((prev) => prev.map((e) => (e.id === id ? { ...e, ...patch } : e)));
  }, []);

  const handleRemove = useCallback((id: string) => {
    setEntries((prev) => prev.filter((e) => e.id !== id));
  }, []);

  const handleAdd = () => {
    setEntries((prev) => [
      ...prev,
      {
        id: uniqueId(),
        provider_id: "",
        provider: "",
        model: "",
        enabled: true,
        timeout: 120,
        max_retries: 2,
        params: {},
      },
    ]);
  };

  const handleSave = async () => {
    setSaving(true);
    try {
      const serialized = entries.map(({ id: _id, ...rest }) => rest);
      await onSave(buildChainSettings(initialSettings, serialized, isReadImage ? imageInput : null));
    } catch {
      // toast shown by hook
    } finally {
      setSaving(false);
    }
  };

  return (
    <div ref={portalRef} className="relative">
      <DialogHeader>
        <DialogTitle>{formatToolTitle(toolName)} {t("builtin.mediaChain.providerChainSuffix")}</DialogTitle>
        <DialogDescription>
          {t("builtin.mediaChain.description")}
        </DialogDescription>
      </DialogHeader>

      <div className="space-y-2 max-h-[60vh] overflow-y-auto pr-1 my-4">
        <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={handleDragEnd}>
          <SortableContext items={entries.map((e) => e.id)} strategy={verticalListSortingStrategy}>
            {entries.map((entry, index) => (
              <SortableProviderCard
                key={entry.id}
                entry={entry}
                index={index}
                toolName={toolName}
                enabledProviders={enabledProviders}
                onUpdate={handleUpdate}
                onRemove={handleRemove}
                portalRef={portalRef}
              />
            ))}
          </SortableContext>
        </DndContext>

        {entries.length === 0 && (
          <p className="text-sm text-muted-foreground text-center py-6">
            {t("builtin.mediaChain.noProviders")}
          </p>
        )}

        <Button type="button" variant="outline" size="sm" className="w-full" onClick={handleAdd}>
          <Plus className="size-3.5 mr-1.5" />
          {t("builtin.mediaChain.addProvider")}
        </Button>

        {isReadImage && (
          <div className="space-y-1.5 pt-2">
            <Label>{t("builtin.mediaChain.imageInput")}</Label>
            <Select value={imageInput} onValueChange={(value) => setImageInput(value as ImageInputMode)}>
              <SelectTrigger className="text-base md:text-sm">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="auto">{t("builtin.mediaChain.imageInputAuto")}</SelectItem>
                <SelectItem value="inline">{t("builtin.mediaChain.imageInputInline")}</SelectItem>
                <SelectItem value="file_ref">{t("builtin.mediaChain.imageInputFileRef")}</SelectItem>
              </SelectContent>
            </Select>
            <p className="text-xs text-muted-foreground">{t("builtin.mediaChain.imageInputHint")}</p>
          </div>
        )}
      </div>

      <DialogFooter>
        <Button variant="outline" onClick={onCancel}>
          {t("builtin.mediaChain.cancel")}
        </Button>
        <Button onClick={handleSave} disabled={saving}>
          {saving && <Loader2 className="h-4 w-4 animate-spin" />}
          {saving ? t("builtin.mediaChain.saving") : t("builtin.mediaChain.save")}
        </Button>
      </DialogFooter>
    </div>
  );
}
