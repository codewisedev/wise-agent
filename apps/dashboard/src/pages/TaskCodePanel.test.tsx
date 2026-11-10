import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { DiffView } from "./TaskCodePanel";

const patch = `diff --git a/src/greet.js b/src/greet.js
index a106507..ece99cb 100644
--- a/src/greet.js
+++ b/src/greet.js
@@ -1,3 +1,3 @@
 export function greet(name) {
-  return \`Hello, \${name}!\`;
--- a SQL comment that was removed
+  return \`Hello, \${name.trim()}!\`;
 }
diff --git a/test/greet.test.js b/test/greet.test.js
new file mode 100644
--- /dev/null
+++ b/test/greet.test.js
@@ -0,0 +1 @@
+test("trims", () => {});
`;

describe("DiffView", () => {
  it("shows each file with added and removed lines marked", () => {
    const { container } = render(<DiffView patch={patch} />);
    expect(screen.getByText("src/greet.js")).toBeInTheDocument();
    expect(screen.getByText("test/greet.test.js")).toBeInTheDocument();
    expect(container.querySelectorAll(".diff-add")).toHaveLength(2);
    expect(container.querySelectorAll(".diff-del")).toHaveLength(2);
    expect(container.querySelectorAll(".diff-hunk")).toHaveLength(2);
  });

  it("says when there are no changes", () => {
    render(<DiffView patch="" />);
    expect(screen.getByText("No changes.")).toBeInTheDocument();
  });
});
