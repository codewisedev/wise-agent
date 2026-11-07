import { useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { api, unwrap, type Schemas } from "./client";

export type AgentEvent = Schemas["AgentEvent"];

function wsURL(runId: string): string {
  const proto = window.location.protocol === "https:" ? "wss" : "ws";
  return `${proto}://${window.location.host}/api/v1/events/ws?runId=${encodeURIComponent(runId)}`;
}

/**
 * Events for a run: loaded over REST, then streamed live over WebSocket.
 * Events are merged by sequence number, so reconnects never duplicate them.
 */
export function useRunEvents(runId: string, live: boolean) {
  const [byseq, setBySeq] = useState<Map<number, AgentEvent>>(new Map());
  const [connected, setConnected] = useState(false);
  const lastSeq = useRef(0);

  const merge = (evs: AgentEvent[]) => {
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

  const initial = useQuery({
    queryKey: ["run", runId, "events"],
    queryFn: () => unwrap(api.GET("/agent-runs/{runId}/events", { params: { path: { runId } } })),
  });
  useEffect(() => {
    if (initial.data) merge(initial.data.items);
  }, [initial.data]);

  useEffect(() => {
    if (!live) return;
    let ws: WebSocket | null = null;
    let closed = false;
    let retry: number | undefined;

    const catchUp = async () => {
      const data = await unwrap(
        api.GET("/agent-runs/{runId}/events", { params: { path: { runId }, query: { afterSeq: lastSeq.current } } }),
      ).catch(() => null);
      if (data) merge(data.items);
    };
    const connect = () => {
      ws = new WebSocket(wsURL(runId));
      ws.onopen = () => {
        setConnected(true);
        void catchUp();
      };
      ws.onmessage = (m) => {
        try {
          merge([JSON.parse(m.data as string) as AgentEvent]);
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
  }, [runId, live]);

  const events = [...byseq.values()].sort((a, b) => a.seq - b.seq);
  return { events, connected, error: initial.error, loading: initial.isPending };
}
