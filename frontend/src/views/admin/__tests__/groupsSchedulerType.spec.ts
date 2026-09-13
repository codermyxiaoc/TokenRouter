import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";

import { describe, expect, it } from "vitest";

const currentDir = dirname(fileURLToPath(import.meta.url));
const groupsViewSource = readFileSync(
  resolve(currentDir, "../GroupsView.vue"),
  "utf8",
);

describe("groups scheduler type", () => {
  it("uses the shared Select control and preserves the scheduler mode in both forms", () => {
    expect(groupsViewSource).toContain('v-model="createForm.scheduler_type"');
    expect(groupsViewSource).toContain('v-model="editForm.scheduler_type"');
    expect(groupsViewSource).toContain(':options="schedulerTypeOptions"');
    expect(groupsViewSource).toContain('scheduler_type: "basic" as GroupSchedulerType');
    expect(groupsViewSource).toContain('group.scheduler_type ?? "basic"');
    expect(groupsViewSource).toContain('advanced_scheduler_overrides');
    expect(groupsViewSource).toContain('GroupAdvancedSchedulerOverridesModal');
    expect(groupsViewSource).not.toMatch(/<select\b/);
  });
});
