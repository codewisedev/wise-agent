import { Navigate, createBrowserRouter } from "react-router";
import { Layout } from "./components/Layout";
import { AgentDetailPage } from "./pages/AgentDetailPage";
import { AgentsPage } from "./pages/AgentsPage";
import { ModelsPage } from "./pages/ModelsPage";
import { RunPage } from "./pages/RunPage";
import { RequireAuth } from "./components/RequireAuth";
import { LoginPage } from "./pages/LoginPage";
import { ProjectDetailPage } from "./pages/ProjectDetailPage";
import { ProjectsPage } from "./pages/ProjectsPage";
import { SettingsPage } from "./pages/SettingsPage";

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
      { path: "/agents", element: <AgentsPage /> },
      { path: "/agents/:agentName", element: <AgentDetailPage /> },
      { path: "/runs/:runId", element: <RunPage /> },
      { path: "/models", element: <ModelsPage /> },
      { path: "/settings", element: <SettingsPage /> },
      { path: "*", element: <p>Page not found.</p> },
    ],
  },
]);
