import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";
import { ApiError, api, setCsrfToken, unwrap, type Schemas } from "./api/client";

export const meKey = ["auth", "me"] as const;

async function fetchMe(): Promise<Schemas["SessionInfo"] | null> {
  try {
    const me = await unwrap(api.GET("/auth/me"));
    setCsrfToken(me.csrfToken);
    return me;
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      setCsrfToken(undefined);
      return null;
    }
    throw err;
  }
}

/** Current session, or null when logged out. */
export function useSession() {
  const qc = useQueryClient();
  useEffect(() => {
    const onUnauthorized = () => {
      setCsrfToken(undefined);
      qc.setQueryData(meKey, null);
    };
    window.addEventListener("wise:unauthorized", onUnauthorized);
    return () => window.removeEventListener("wise:unauthorized", onUnauthorized);
  }, [qc]);
  return useQuery({ queryKey: meKey, queryFn: fetchMe, staleTime: 60_000, retry: false });
}

export function useLogin() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Schemas["LoginRequest"]) => unwrap(api.POST("/auth/login", { body })),
    onSuccess: (session) => {
      setCsrfToken(session.csrfToken);
      qc.setQueryData(meKey, session);
    },
  });
}

export function useLogout() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => unwrap(api.POST("/auth/logout")),
    onSettled: () => {
      setCsrfToken(undefined);
      qc.clear();
      qc.setQueryData(meKey, null);
    },
  });
}
