// UI messages that JS needs to render live in body data attributes,
// translated server-side (see the <body> tag in layout.html).
export const msg = (key: string): string => document.body.dataset[key] || key;

// Shared metric formatters, used by both the dashboard and the per-
// container card. CPU percentages are null until an SSE stream has two
// samples to diff, and render as an em dash.
export const fmtBytes = (n: number): string => {
  if (!n) return "0 B";
  const u = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return (i === 0 ? n : n.toFixed(1)) + " " + u[i];
};

export const fmtPct = (v: number | null): string => (v == null ? "—" : v.toFixed(0) + "%");
