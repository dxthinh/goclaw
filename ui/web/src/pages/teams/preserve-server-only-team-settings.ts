import type { TeamAccessSettings } from "@/types/team";

/**
 * Khóa settings mà form Team Settings không chỉnh nhưng server vẫn lưu.
 * teams.update ghi đè cả object settings, nên các khóa này phải được chép lại
 * từ bản hiện có, nếu không mỗi lần lưu form sẽ xóa chúng (ví dụ tắt task_contract
 * đã bật qua RPC).
 */
const SERVER_ONLY_KEYS = ["version", "task_contract"] as const;

export function preserveServerOnlyTeamSettings(
  initial: TeamAccessSettings,
  settings: TeamAccessSettings,
): TeamAccessSettings {
  const out: TeamAccessSettings = { ...settings };
  for (const key of SERVER_ONLY_KEYS) {
    if (initial[key] !== undefined) {
      (out as Record<string, unknown>)[key] = initial[key];
    }
  }
  return out;
}
