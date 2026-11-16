/**
 * Ô chọn người nhận của form tạo task không được có lead: chọn lead thì
 * teams.tasks.create vẫn thành công nhưng dispatch đánh task failed ngay.
 * Render DOM thật (createRoot + jsdom) vì Dialog dùng portal.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { TeamMemberData } from "@/types/team";
import { CreateTaskDialog } from "./create-task-dialog";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const members: TeamMemberData[] = [
  { team_id: "team", agent_id: "lead-id", agent_key: "boss", display_name: "Boss", role: "lead" },
  { team_id: "team", agent_id: "member-id", agent_key: "analyst", display_name: "Analyst", role: "member" },
  { team_id: "team", agent_id: "reviewer-id", agent_key: "checker", role: "reviewer" },
];

let root: Root | null = null;
let host: HTMLDivElement | null = null;

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null; host = null;
});

function renderDialog(list: TeamMemberData[]) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => {
    root!.render(
      <CreateTaskDialog
        open
        onOpenChange={() => {}}
        teamId="team"
        members={list}
        selectedScope={null}
        taskContractEnabled={false}
        createTask={vi.fn()}
        onCreated={() => {}}
      />,
    );
  });
  return document.querySelector<HTMLSelectElement>("#task-assignee");
}

describe("CreateTaskDialog assignee selector", () => {
  it("liệt kê member và reviewer, không có lead", () => {
    const select = renderDialog(members);
    expect(select).not.toBeNull();
    const values = Array.from(select!.options).map((o) => o.value);
    expect(values).toEqual(["", "member-id", "reviewer-id"]);
    expect(select!.textContent).not.toContain("Boss");
  });

  it("ẩn ô chọn khi team chỉ có lead", () => {
    expect(renderDialog(members.slice(0, 1))).toBeNull();
  });
});
