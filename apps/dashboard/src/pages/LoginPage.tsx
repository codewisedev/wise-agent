import { LogIn } from "lucide-react";
import { useState, type FormEvent } from "react";
import { Navigate, useLocation, useNavigate } from "react-router";
import { useLogin, useSession } from "../auth";
import { ErrorNote, Field } from "../components/ui";

export function LoginPage() {
  const { data: session } = useSession();
  const login = useLogin();
  const navigate = useNavigate();
  const location = useLocation();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const from = (location.state as { from?: string } | null)?.from ?? "/projects";

  if (session) return <Navigate to={from} replace />;

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    login.mutate({ username, password }, { onSuccess: () => navigate(from, { replace: true }) });
  };

  return (
    <div className="login">
      <form className="login-card" onSubmit={onSubmit}>
        <div>
          <div className="brand">
            <span className="brand-mark" aria-hidden>
              &gt;_
            </span>
            wise
          </div>
          <p className="muted small" style={{ marginTop: 6 }}>
            Sign in to your engineering platform.
          </p>
        </div>
        <Field label="Username">
          <input autoFocus autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} required />
        </Field>
        <Field label="Password">
          <input
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
          />
        </Field>
        <ErrorNote error={login.error} />
        <button type="submit" className="btn btn-primary btn-block" disabled={login.isPending}>
          <LogIn size={15} aria-hidden />
          {login.isPending ? "Signing in…" : "Sign in"}
        </button>
        <div className="login-foot">
          <span className="mono">local · self-hosted</span>
          <span className="mono">v0.1</span>
        </div>
      </form>
    </div>
  );
}
