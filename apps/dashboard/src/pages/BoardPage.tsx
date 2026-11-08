import { useQuery, useQueryClient } from "@tanstack/react-query";
import { CalendarRange, ChevronRight, Plug } from "lucide-react";
import { Link, useParams } from "react-router";
import { api, unwrap } from "../api/client";
import { useEventSocket, type WorkflowEvent } from "../api/events";
import { Board } from "../components/Board";
import { ErrorNote } from "../components/ui";

export { positionBetween } from "../components/Board";

export function BoardPage() {
  const { projectId = "" } = useParams();
  const qc = useQueryClient();
  const project = useQuery({
    queryKey: ["project", projectId],
    queryFn: () => unwrap(api.GET("/projects/{projectId}", { params: { path: { projectId } } })),
  });
  const board = useQuery({
    queryKey: ["board", projectId],
    queryFn: () => unwrap(api.GET("/projects/{projectId}/board", { params: { path: { projectId } } })),
  });
  // Workflows move cards: refresh on any workflow status change.
  useEventSocket<WorkflowEvent>("stream=workflows", true, (ev) => {
    if (ev.type === "workflow.status" || ev.type.startsWith("approval.")) void qc.invalidateQueries({ queryKey: ["board", projectId] });
  });

  return (
    <>
      <nav className="crumbs" aria-label="Breadcrumb">
        <Link to="/projects">projects</Link>
        <ChevronRight size={12} aria-hidden />
        <Link to={`/projects/${projectId}`}>{project.data?.name ?? "project"}</Link>
        <ChevronRight size={12} aria-hidden />
        <span>board</span>
      </nav>
      <header className="page-header">
        <div>
          <h1>{project.data?.name ?? "Board"}</h1>
          <p className="desc">Drag cards and lists. Click a list name to rename it; ⋯ sets its role, color and WIP limit. Runs move cards by role.</p>
        </div>
        <div className="actions" style={{ margin: 0 }}>
          <Link to={`/projects/${projectId}/sprints`} className="btn btn-sm">
            <CalendarRange size={14} aria-hidden /> Sprints
          </Link>
          <Link to={`/projects/${projectId}/integrations`} className="btn btn-sm">
            <Plug size={14} aria-hidden /> Task sources
          </Link>
        </div>
      </header>
      <ErrorNote error={board.error} />
      {board.data && <Board lists={board.data.columns} projectId={projectId} queryKey={["board", projectId]} />}
    </>
  );
}
