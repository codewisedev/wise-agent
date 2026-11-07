import { useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { api, unwrap, type Schemas } from "./client";

export type AgentEvent = Schemas["AgentEvent"];
export type WorkflowEvent = Schemas["WorkflowEvent"];

function wsURL(query: string): string {
  const proto = window.location.protocol === "https:" ? "wss" : "ws";
  return `${proto}://${window.location.host}/api/v1/events/ws?${query}`;
}

/**
 * Opens the events WebSocket and reconnects after drops. onOpen runs on every
 * (re)connect so callers can catch up over REST.
 */
export function useEventSocket<T>(query: string, live: boolean, onMessage: (ev: T) => void, onOpen?: () => void) {
  const [connected, setConnected] = useState(false);
  const handlers = useRef({ onMessage, onOpen });
  handlers.current = { onMessage, onOpen };

  useEffect(() => {
    if (!live) return;
    let ws: WebSocket | null = null;
    let closed = false;
    let retry: number | undefined;
    const connect = () => {
      ws = new WebSocket(wsURL(query));
      ws.onopen = () => {
        setConnected(true);
        handlers.current.onOpen?.();
      };
      ws.onmessage = (m) => {
        try {
          handlers.current.onMessage(JSON.parse(m.data as string) as T);
        } catch {
          /* ignore malformed frames */
        }
      };
      ws.onclose = () => {
        setConnected(false);
        if (!closed) retry = window.setTimeout(connect, 2000);
      };
    };
    connect();
    return () => {
      closed = true;
      window.clearTimeout(retry);
      ws?.close();
    };
  }, [query, live]);
  return connected;
}

/**
 * Ordered events: loaded over REST, then streamed live over WebSocket. Events
 * are merged by sequence number, so reconnects never duplicate them.
 */
function useSeqEvents<T extends { seq: number }>(opts: {
  key: unknown[];
  load: (afterSeq: number) => Promise<{ items: T[] }>;
  socketQuery: string;
  live: boolean;
  enabled?: boolean;
}) {
  const [byseq, setBySeq] = useState<Map<number, T>>(new Map());
  const lastSeq = useRef(0);
  // A different stream (e.g. a new workflow run on the same page) starts empty.
  const keyId = JSON.stringify(opts.key);
  useEffect(() => {
    setBySeq(new Map());
    lastSeq.current = 0;
  }, [keyId]);

  const merge = (evs: T[]) => {
    if (!evs.length) return;
    setBySeq((prev) => {
      const next = new Map(prev);
      for (const e of evs) {
        next.set(e.seq, e);
        lastSeq.current = Math.max(lastSeq.current, e.seq);
      }
      return next;
    });
  };

  const enabled = opts.enabled ?? true;
  const initial = useQuery({ queryKey: opts.key, queryFn: () => opts.load(0), enabled });
  useEffect(() => {
    if (initial.data) merge(initial.data.items);
  }, [initial.data]);

  const connected = useEventSocket<T>(
    opts.socketQuery,
    opts.live && enabled,
    (ev) => merge([ev]),
    () => void opts.load(lastSeq.current).then((d) => merge(d.items)).catch(() => undefined),
  );

  const events = [...byseq.values()].sort((a, b) => a.seq - b.seq);
  return { events, connected, error: initial.error, loading: enabled && initial.isPending };
}

export function useRunEvents(runId: string, live: boolean) {
  return useSeqEvents<AgentEvent>({
    key: ["run", runId, "events"],
    load: (afterSeq) =>
      unwrap(api.GET("/agent-runs/{runId}/events", { params: { path: { runId }, query: { afterSeq } } })),
    socketQuery: `runId=${encodeURIComponent(runId)}`,
    live,
  });
}

export function useWorkflowEvents(workflowId: string, live: boolean) {
  return useSeqEvents<WorkflowEvent>({
    key: ["workflow", workflowId, "events"],
    load: (afterSeq) =>
      unwrap(api.GET("/workflows/{workflowId}/events", { params: { path: { workflowId }, query: { afterSeq } } })),
    socketQuery: `workflowId=${encodeURIComponent(workflowId)}`,
    live,
    enabled: !!workflowId,
  });
}
