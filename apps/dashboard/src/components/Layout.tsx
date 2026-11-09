import { useQueryClient } from "@tanstack/react-query";
import { Bot, Brain, Cpu, FolderGit2, Inbox, ListChecks, LogOut, Monitor, Moon, Settings, Sun, Workflow } from "lucide-react";
import { useMemo } from "react";
import { NavLink, Outlet, useNavigate } from "react-router";
import { useEventSocket, type WorkflowEvent } from "../api/events";
import { useLogout, useSession } from "../auth";
import { usePendingApprovals } from "../pages/ApprovalsPage";
import { useMemoryInbox } from "../pages/MemoryPage";
import { useShortcuts } from "../shortcuts";
import { useTheme } from "../theme";
import { Kbd } from "./ui";

const THEME_META = {
  dark: { icon: Moon, label: "Dark" },
  light: { icon: Sun, label: "Light" },
  system: { icon: Monitor, label: "System" },
} as const;

export function Layout() {
  const { data: session } = useSession();
  const logout = useLogout();
  const navigate = useNavigate();
  const { pref, cycle } = useTheme();
  const ThemeIcon = THEME_META[pref].icon;
  const qc = useQueryClient();
  const pending = usePendingApprovals();
  const pendingCount = pending.data?.pendingCount ?? 0;
  const memoryInbox = useMemoryInbox().data?.candidates ?? 0;
  // Every workflow's events: keeps the approvals badge and inbox current.
  useEventSocket<WorkflowEvent>("stream=workflows", true, (ev) => {
    if (ev.type.startsWith("approval.") || ev.type === "workflow.status") {
      void qc.invalidateQueries({ queryKey: ["approvals"] });
      void qc.invalidateQueries({ queryKey: ["tasks"] });
    }
  });

  const bindings = useMemo(
    () => ({
      "g p": () => navigate("/projects"),
      "g t": () => navigate("/tasks"),
      "g i": () => navigate("/approvals"),
      "g w": () => navigate("/workflows"),
      "g a": () => navigate("/agents"),
      "g y": () => navigate("/memory"),
      "g m": () => navigate("/models"),
      "g s": () => navigate("/settings"),
      t: cycle,
    }),
    [navigate, cycle],
  );
  useShortcuts(bindings);

  const username = session?.user.username ?? "";
  return (
    <div className="shell">
      <aside className="sidebar">
        <NavLink to="/" className="brand" aria-label="Wise home">
          <span className="brand-mark" aria-hidden>
            &gt;_
          </span>
          wise
          <span className="brand-sub">v0.1</span>
        </NavLink>
        <nav className="nav" aria-label="Main">
          <NavLink to="/projects">
            <FolderGit2 size={16} aria-hidden /> Projects <Kbd keys={["g", "p"]} />
          </NavLink>
          <NavLink to="/tasks">
            <ListChecks size={16} aria-hidden /> Tasks <Kbd keys={["g", "t"]} />
          </NavLink>
          <NavLink to="/approvals">
            <Inbox size={16} aria-hidden /> Approvals
            {pendingCount > 0 && (
              <span className="nav-count" aria-label={`${pendingCount} pending`}>
                {pendingCount}
              </span>
            )}
            <Kbd keys={["g", "i"]} />
          </NavLink>
          <NavLink to="/workflows">
            <Workflow size={16} aria-hidden /> Workflows <Kbd keys={["g", "w"]} />
          </NavLink>
          <NavLink to="/agents">
            <Bot size={16} aria-hidden /> Agents <Kbd keys={["g", "a"]} />
          </NavLink>
          <NavLink to="/memory">
            <Brain size={16} aria-hidden /> Memory
            {memoryInbox > 0 && (
              <span className="nav-count" aria-label={`${memoryInbox} to review`}>
                {memoryInbox}
              </span>
            )}
            <Kbd keys={["g", "y"]} />
          </NavLink>
          <NavLink to="/models">
            <Cpu size={16} aria-hidden /> Models <Kbd keys={["g", "m"]} />
          </NavLink>
          <NavLink to="/settings">
            <Settings size={16} aria-hidden /> Settings <Kbd keys={["g", "s"]} />
          </NavLink>
        </nav>
        <div className="sidebar-foot">
          <button
            type="button"
            className="btn btn-ghost btn-sm"
            onClick={cycle}
            aria-label={`Theme: ${THEME_META[pref].label}. Switch theme`}
            title="Switch theme (t)"
          >
            <ThemeIcon size={14} aria-hidden /> {THEME_META[pref].label}
          </button>
          <div className="user-chip">
            <span className="avatar" aria-hidden>
              {username.slice(0, 2)}
            </span>
            <span className="mono">{username}</span>
            <button
              type="button"
              className="btn btn-ghost btn-icon"
              style={{ marginLeft: "auto" }}
              aria-label="Sign out"
              title="Sign out"
              onClick={() => logout.mutate(undefined, { onSettled: () => navigate("/login") })}
            >
              <LogOut size={14} aria-hidden />
            </button>
          </div>
        </div>
      </aside>
      <main className="content">
        <Outlet />
      </main>
    </div>
  );
}
