import { Navigate, createBrowserRouter } from "react-router";
import { Layout } from "./components/Layout";
import { AgentDetailPage } from "./pages/AgentDetailPage";
import { AgentsPage } from "./pages/AgentsPage";
import { BoardPage } from "./pages/BoardPage";
import { ContextPage } from "./pages/ContextPage";
import { MemoryPage } from "./pages/MemoryPage";
import { ProfilePage } from "./pages/ProfilePage";
import { SprintsPage } from "./pages/SprintsPage";
import { IntegrationsPage } from "./pages/IntegrationsPage";
import { ApprovalsPage } from "./pages/ApprovalsPage";
import { ModelsPage } from "./pages/ModelsPage";
import { RunPage } from "./pages/RunPage";
import { RequireAuth } from "./components/RequireAuth";
import { LoginPage } from "./pages/LoginPage";
import { ProjectDetailPage } from "./pages/ProjectDetailPage";
import { ProjectsPage } from "./pages/ProjectsPage";
import { SettingsPage } from "./pages/SettingsPage";
import { TaskPage } from "./pages/TaskPage";
import { WorkflowsPage } from "./pages/WorkflowsPage";
import { TasksPage } from "./pages/TasksPage";

export const router = createBrowserRouter([
  { path: "/login", element: <LoginPage /> },
  {
    element: (
      <RequireAuth>
        <Layout />
      </RequireAuth>
    ),
    children: [
      { index: true, element: <Navigate to="/projects" replace /> },
      { path: "/projects", element: <ProjectsPage /> },
      { path: "/projects/:projectId", element: <ProjectDetailPage /> },
      { path: "/projects/:projectId/board", element: <BoardPage /> },
      { path: "/projects/:projectId/integrations", element: <IntegrationsPage /> },
      { path: "/projects/:projectId/sprints", element: <SprintsPage /> },
      { path: "/projects/:projectId/context", element: <ContextPage /> },
      { path: "/tasks", element: <TasksPage /> },
      { path: "/tasks/:taskId", element: <TaskPage /> },
      { path: "/approvals", element: <ApprovalsPage /> },
      { path: "/workflows", element: <WorkflowsPage /> },
      { path: "/agents", element: <AgentsPage /> },
      { path: "/memory", element: <MemoryPage /> },
      { path: "/profile", element: <ProfilePage /> },
      { path: "/agents/:agentName", element: <AgentDetailPage /> },
      { path: "/runs/:runId", element: <RunPage /> },
      { path: "/models", element: <ModelsPage /> },
      { path: "/settings", element: <SettingsPage /> },
      { path: "*", element: <p>Page not found.</p> },
    ],
  },
]);
