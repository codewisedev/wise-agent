import { useQuery } from "@tanstack/react-query";
import { Inbox } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router";
import { api, unwrap, type Schemas } from "../api/client";
import { Badge, Empty, ErrorNote, Panel, label, relativeTime, type Tone } from "../components/ui";
import { KIND_LABEL } from "./workflowUi";

const tone: Record<Schemas["ApprovalStatus"], Tone> = {
  pending: "warn",
  approved: "ok",
  rejected: "bad",
  superseded: "neutral",
  cancelled: "neutral",
};

/** The pending-approvals count, shared by the sidebar and the inbox. */
export function usePendingApprovals() {
  return useQuery({
    queryKey: ["approvals", "pending"],
    queryFn: () => unwrap(api.GET("/approvals", { params: { query: { status: "pending" } } })),
    refetchInterval: 15000,
  });
}

export function ApprovalsPage() {
  const [showAll, setShowAll] = useState(false);
  const pending = usePendingApprovals();
  const all = useQuery({
    queryKey: ["approvals", "all"],
    queryFn: () => unwrap(api.GET("/approvals")),
    enabled: showAll,
  });
  const data = showAll ? all.data : pending.data;

  return (
    <>
      <header className="page-header">
        <div>
          <h1>Approvals</h1>
          <p className="desc">Scope and plan decisions waiting for you. Each approval pins exact artifact versions.</p>
        </div>
      </header>
      <Panel
        title={showAll ? "All approvals" : "Pending"}
        count={data?.items.length}
        actions={
          <button type="button" className="btn btn-ghost btn-sm" onClick={() => setShowAll((v) => !v)} aria-pressed={showAll}>
            {showAll ? "Pending only" : "Show history"}
          </button>
        }
      >
        <ErrorNote error={showAll ? all.error : pending.error} />
        {data && data.items.length === 0 && <Empty icon={<Inbox size={24} aria-hidden />}>Nothing is waiting for you.</Empty>}
        {data && data.items.length > 0 && (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Task</th>
                  <th>Gate</th>
                  <th>Pins</th>
                  <th>Status</th>
                  <th>Requested</th>
                </tr>
              </thead>
              <tbody>
                {data.items.map((a) => (
                  <tr key={a.id}>
                    <td>
                      <Link to={`/tasks/${a.taskId}`} className="row-link">{a.taskTitle}</Link>
                      <div className="cell-sub small faint">{a.projectName}</div>
                    </td>
                    <td className="small">{a.gate} · round {a.round}</td>
                    <td className="small">
                      {a.artifacts.map((r) => (
                        <span key={r.artifactId} className="chip">{KIND_LABEL[r.kind]} v{r.version}</span>
                      ))}
                    </td>
                    <td><Badge tone={tone[a.status]}>{label(a.status)}</Badge></td>
                    <td className="small faint">{relativeTime(a.createdAt)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Panel>
    </>
  );
}
