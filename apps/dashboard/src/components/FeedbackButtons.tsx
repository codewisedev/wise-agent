import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ThumbsDown, ThumbsUp } from "lucide-react";
import { useEffect, useState } from "react";
import { api, unwrap } from "../api/client";
import { useSession } from "../auth";
import { ErrorNote } from "./ui";

type Target = "run" | "artifact";

/** Your good/bad rating of a run or artifact, with an optional note (feeds Insights and improvement analyses). */
export function FeedbackButtons({ targetType, targetId }: { targetType: Target; targetId: string }) {
  const qc = useQueryClient();
  const { data: session } = useSession();
  const key = ["feedback", targetType, targetId];
  const list = useQuery({
    queryKey: key,
    queryFn: () => unwrap(api.GET("/feedback", { params: { query: { targetType, targetId } } })),
  });
  const mine = list.data?.items.find((f) => f.userId === session?.user.id);
  const [note, setNote] = useState("");
  const [open, setOpen] = useState(false);
  useEffect(() => setNote(mine?.note ?? ""), [mine?.note]);
  const set = useMutation({
    mutationFn: (rating: "up" | "down") => unwrap(api.PUT("/feedback", { body: { targetType, targetId, rating, note } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: key }),
  });
  const clear = useMutation({
    mutationFn: () => unwrap(api.DELETE("/feedback", { params: { query: { targetType, targetId } } })),
    onSuccess: () => {
      setNote("");
      void qc.invalidateQueries({ queryKey: key });
    },
  });
  const rate = (r: "up" | "down") => {
    if (mine?.rating === r && !open) clear.mutate();
    else set.mutate(r);
    if (r === "down" && !mine?.note) setOpen(true);
  };
  return (
    <span className="feedback" role="group" aria-label={`Rate this ${targetType}`}>
      <button type="button" className={`btn btn-ghost btn-icon${mine?.rating === "up" ? " active" : ""}`} aria-pressed={mine?.rating === "up"}
        aria-label="Good result" title="Good result" onClick={() => rate("up")} disabled={set.isPending || clear.isPending}>
        <ThumbsUp size={14} aria-hidden />
      </button>
      <button type="button" className={`btn btn-ghost btn-icon${mine?.rating === "down" ? " active" : ""}`} aria-pressed={mine?.rating === "down"}
        aria-label="Bad result" title="Bad result" onClick={() => rate("down")} disabled={set.isPending || clear.isPending}>
        <ThumbsDown size={14} aria-hidden />
      </button>
      {mine && !open && (
        <button type="button" className="btn btn-ghost btn-sm" onClick={() => setOpen(true)}>{mine.note ? "edit note" : "add note"}</button>
      )}
      {open && (
        <form className="feedback-note" onSubmit={(e) => { e.preventDefault(); set.mutate(mine?.rating ?? "down"); setOpen(false); }}>
          <input aria-label="What was good or wrong" placeholder="What was good or wrong? (optional)" value={note} maxLength={2000}
            onChange={(e) => setNote(e.target.value)} />
          <button type="submit" className="btn btn-sm">Save</button>
        </form>
      )}
      <ErrorNote error={set.error ?? clear.error} />
    </span>
  );
}
