import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

const currentDir = dirname(fileURLToPath(import.meta.url));
const groupsViewSource = readFileSync(
  resolve(currentDir, "../GroupsView.vue"),
  "utf8",
);

describe("groups session isolation", () => {
  it("keeps the switch in create/edit forms and includes it in payload spread", () => {
    expect(groupsViewSource).toContain('key: "session_isolation_enabled"');
    expect(groupsViewSource).toContain("createForm.session_isolation_enabled");
    expect(groupsViewSource).toContain("editForm.session_isolation_enabled");
    expect(groupsViewSource).toContain(
      "group.session_isolation_enabled ?? false",
    );
    expect(groupsViewSource).toContain("...createForm");
    expect(groupsViewSource).toContain("...editForm");
  });
});
