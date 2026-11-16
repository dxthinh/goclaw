import type { TeamAccessSettings } from "@/types/team";

/**
 * Khóa settings mà form Team Settings có thể không ghi nhưng server vẫn lưu.
 * teams.update ghi đè cả object settings, nên khóa nào form không ghi phải được
 * chép lại từ bản hiện có, nếu không mỗi lần lưu form sẽ xóa chúng.
 * - version: form không bao giờ chỉnh (tránh hạ v2 xuống v1).
 * - task_contract: form ghi khi có công tắc (bản Standard); bản Lite ẩn công
 *   tắc nên phải giữ giá trị đang có.
 * Khóa form đã ghi luôn thắng bản cũ.
 */
const SERVER_ONLY_KEYS = ["version", "task_contract"] as const;

export function preserveServerOnlyTeamSettings(
  initial: TeamAccessSettings,
  settings: TeamAccessSettings,
): TeamAccessSettings {
  const out: TeamAccessSettings = { ...settings };
  for (const key of SERVER_ONLY_KEYS) {
    if (initial[key] !== undefined && out[key] === undefined) {
      (out as Record<string, unknown>)[key] = initial[key];
    }
  }
  return out;
}
