import { fmtBytes, fmtPct, msg } from "./util";

// Shapes of the JSON samples pushed by the metrics SSE endpoints
// (internal/server/sse.go). cpuPct is null until the stream has two
// samples to diff.
interface HostSample {
  load1: number;
  load5: number;
  load15: number;
  cpuPct: number | null;
  memUsed: number;
  memTotal: number;
  diskUsed: number;
  diskTotal: number;
}

interface ContainerSample {
  name: string;
  running: boolean;
  cpuPct: number | null;
  memBytes: number;
  tasks: number;
}

interface MetricsSample {
  host: HostSample;
  containers: ContainerSample[] | null;
}

// Live host + container metrics: the dashboard's #host-metrics card and
// #container-metrics table are fed by an SSE stream that pushes one JSON
// "sample" every couple of seconds.
function watchMetrics(): void {
  const host = document.getElementById("host-metrics");
  const rows = document.getElementById("container-metrics");
  if (!host || !rows) return;

  const pct = fmtPct;
  const setBar = (id: string, v: number | null) => {
    const bar = document.getElementById(id);
    if (bar) bar.style.width = Math.min(100, Math.max(0, v || 0)) + "%";
  };
  const ratio = (used: number, total: number) => (total ? (used / total) * 100 : 0);

  // Whole-row navigation to the container page (the name is also a real
  // anchor, so keyboard focus and modifier-click open a new tab as usual;
  // let those clicks fall through rather than double-handling them).
  rows.addEventListener("click", (ev) => {
    const target = ev.target as Element;
    if (target.closest("a")) return;
    const tr = target.closest<HTMLElement>("tr[data-href]");
    if (tr) location.href = tr.dataset.href!;
  });

  const es = new EventSource("/events/metrics");
  es.addEventListener("sample", (ev) => {
    const s: MetricsSample = JSON.parse((ev as MessageEvent).data);
    const h = s.host;

    document.getElementById("m-load")!.textContent =
      `${h.load1.toFixed(2)} · ${h.load5.toFixed(2)} · ${h.load15.toFixed(2)}`;
    document.getElementById("m-cpu")!.textContent = pct(h.cpuPct);
    setBar("m-cpu-bar", h.cpuPct);
    document.getElementById("m-mem")!.textContent =
      `${fmtBytes(h.memUsed)} / ${fmtBytes(h.memTotal)}`;
    setBar("m-mem-bar", ratio(h.memUsed, h.memTotal));
    document.getElementById("m-disk")!.textContent =
      `${fmtBytes(h.diskUsed)} / ${fmtBytes(h.diskTotal)}`;
    setBar("m-disk-bar", ratio(h.diskUsed, h.diskTotal));

    if (!s.containers || !s.containers.length) {
      const tr = document.createElement("tr");
      tr.className = "metrics-empty";
      const td = document.createElement("td");
      td.colSpan = 4;
      td.className = "empty";
      td.textContent = msg("msgNoContainers");
      tr.append(td);
      rows.replaceChildren(tr);
      return;
    }
    rows.replaceChildren(...s.containers.map((c) => {
      const tr = document.createElement("tr");
      tr.dataset.href = "/workloads/" + c.name;
      const cell = (text: string) => {
        const td = document.createElement("td");
        td.textContent = text;
        return td;
      };
      const name = document.createElement("td");
      const link = document.createElement("a");
      link.href = tr.dataset.href;
      const dot = document.createElement("span");
      dot.className = "dot " + (c.running ? "dot-on" : "dot-off");
      link.append(dot, " ", c.name);
      name.append(link);
      tr.append(name, cell(pct(c.cpuPct)), cell(fmtBytes(c.memBytes)),
                cell(c.running ? String(c.tasks) : "—"));
      return tr;
    }));
  });
  es.onerror = () => {};
}

document.addEventListener("DOMContentLoaded", watchMetrics);

// Live per-container usage on the workload page: the #workload-metrics
// card streams one container's CPU/memory/tasks from its own SSE
// endpoint. Non-running containers report "—" for memory and tasks.
function watchWorkloadMetrics(): void {
  const el = document.getElementById("workload-metrics");
  if (!el) return;

  const es = new EventSource("/events/workloads/" + el.dataset.name + "/metrics");
  es.addEventListener("sample", (ev) => {
    const c: ContainerSample = JSON.parse((ev as MessageEvent).data);
    document.getElementById("wm-cpu")!.textContent = c.running ? fmtPct(c.cpuPct) : "—";
    document.getElementById("wm-mem")!.textContent = c.running ? fmtBytes(c.memBytes) : "—";
    document.getElementById("wm-tasks")!.textContent = c.running ? String(c.tasks) : "—";
  });
  es.onerror = () => {};
}

document.addEventListener("DOMContentLoaded", watchWorkloadMetrics);
