import type { ReactNode } from "react";
import { Navigate, useLocation } from "react-router";
import { useSession } from "../auth";
import { ErrorNote } from "./ui";

export function RequireAuth({ children }: { children: ReactNode }) {
  const { data, isPending, error } = useSession();
  const location = useLocation();
  if (isPending) return <p className="loading">Loading…</p>;
  if (error) return <ErrorNote error={error} />;
  if (!data) return <Navigate to="/login" replace state={{ from: location.pathname }} />;
  return <>{children}</>;
}
