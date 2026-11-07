import { FolderGit2, LogOut, Monitor, Moon, Settings, Sun } from "lucide-react";
import { useMemo } from "react";
import { NavLink, Outlet, useNavigate } from "react-router";
import { useLogout, useSession } from "../auth";
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

  const bindings = useMemo(
    () => ({
      "g p": () => navigate("/projects"),
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
