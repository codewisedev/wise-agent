export type DiffLine = { op: "same" | "add" | "del"; text: string };

/** Line diff by longest common subsequence (fine for prompts and settings: a few hundred lines). */
export function lineDiff(before: string, after: string): DiffLine[] {
  const a = before.split("\n");
  const b = after.split("\n");
  const n = a.length;
  const m = b.length;
  const w = m + 1;
  const lcs = new Int32Array((n + 1) * w); // lcs[i*w+j] = LCS of a[i:] and b[j:]
  const at = (i: number, j: number) => lcs[i * w + j] ?? 0;
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      lcs[i * w + j] = a[i] === b[j] ? at(i + 1, j + 1) + 1 : Math.max(at(i + 1, j), at(i, j + 1));
    }
  }
  const out: DiffLine[] = [];
  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    const x = a[i] ?? "";
    const y = b[j] ?? "";
    if (x === y) {
      out.push({ op: "same", text: x });
      i++;
      j++;
    } else if (at(i + 1, j) >= at(i, j + 1)) {
      out.push({ op: "del", text: x });
      i++;
    } else {
      out.push({ op: "add", text: y });
      j++;
    }
  }
  for (; i < n; i++) out.push({ op: "del", text: a[i] ?? "" });
  for (; j < m; j++) out.push({ op: "add", text: b[j] ?? "" });
  return out;
}

/** Collapses unchanged runs longer than 2*context lines, keeping context around changes. */
export function withContext(lines: DiffLine[], context = 3): (DiffLine | { op: "gap"; count: number })[] {
  const keep = lines.map(() => false);
  lines.forEach((l, i) => {
    if (l.op !== "same") for (let k = Math.max(0, i - context); k <= Math.min(lines.length - 1, i + context); k++) keep[k] = true;
  });
  const out: (DiffLine | { op: "gap"; count: number })[] = [];
  let gap = 0;
  lines.forEach((l, i) => {
    if (keep[i]) {
      if (gap) out.push({ op: "gap", count: gap });
      gap = 0;
      out.push(l);
    } else gap++;
  });
  if (gap) out.push({ op: "gap", count: gap });
  return out;
}
