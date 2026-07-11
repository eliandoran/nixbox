// UI messages that JS needs to render live in body data attributes,
// translated server-side (see the <body> tag in layout.html).
const msg = (key) => document.body.dataset[key] || key;

// Live job log: tail the job's SSE stream into the #job-log pre.
// Called from the job-log fragment after HTMX swaps it in.
function watchJob() {
  const pre = document.getElementById("job-log");
  if (!pre || pre.dataset.watching) return;
  pre.dataset.watching = "1";

  const id = pre.dataset.jobId;
  const es = new EventSource("/events/jobs/" + id);

  es.addEventListener("append", (ev) => {
    pre.textContent += ev.data + "\n";
    pre.scrollTop = pre.scrollHeight;
  });

  es.addEventListener("done", (ev) => {
    es.close();
    const badge = document.getElementById("job-status");
    if (badge) {
      badge.textContent = ev.data;
      badge.className = "status status-" + ev.data;
    }
  });

  es.onerror = () => es.close();
}

document.addEventListener("DOMContentLoaded", watchJob);

// Container journal: follow the SSE journalctl stream into
// #container-log; the checkbox switches between the host-side unit
// journal and the journal from inside the container.
function watchContainerLogs() {
  const pre = document.getElementById("container-log");
  if (!pre) return;
  const toggle = document.getElementById("log-source-inside");
  let es;

  function connect() {
    if (es) es.close();
    pre.textContent = "";
    const source = toggle?.checked ? "?source=container" : "";
    es = new EventSource("/workloads/" + pre.dataset.name + "/logs" + source);
    es.addEventListener("append", (ev) => {
      const follow = pre.scrollHeight - pre.scrollTop - pre.clientHeight < 40;
      pre.textContent += ev.data + "\n";
      if (follow) pre.scrollTop = pre.scrollHeight;
    });
  }

  toggle?.addEventListener("change", connect);
  connect();
}

document.addEventListener("DOMContentLoaded", watchContainerLogs);

// Shared metric formatters, used by both the dashboard and the per-
// container card. CPU percentages are null until an SSE stream has two
// samples to diff, and render as an em dash.
const fmtBytes = (n) => {
  if (!n) return "0 B";
  const u = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return (i === 0 ? n : n.toFixed(1)) + " " + u[i];
};
const fmtPct = (v) => (v == null ? "—" : v.toFixed(0) + "%");

// Live host + container metrics: the dashboard's #host-metrics card and
// #container-metrics table are fed by an SSE stream that pushes one JSON
// "sample" every couple of seconds.
function watchMetrics() {
  const host = document.getElementById("host-metrics");
  const rows = document.getElementById("container-metrics");
  if (!host || !rows) return;

  const pct = fmtPct;
  const setBar = (id, v) => {
    const bar = document.getElementById(id);
    if (bar) bar.style.width = Math.min(100, Math.max(0, v || 0)) + "%";
  };
  const ratio = (used, total) => (total ? (used / total) * 100 : 0);

  // Whole-row navigation to the container page (the name is also a real
  // anchor, so keyboard focus and modifier-click open a new tab as usual;
  // let those clicks fall through rather than double-handling them).
  rows.addEventListener("click", (ev) => {
    if (ev.target.closest("a")) return;
    const tr = ev.target.closest("tr[data-href]");
    if (tr) location.href = tr.dataset.href;
  });

  const es = new EventSource("/events/metrics");
  es.addEventListener("sample", (ev) => {
    const s = JSON.parse(ev.data);
    const h = s.host;

    document.getElementById("m-load").textContent =
      `${h.load1.toFixed(2)} · ${h.load5.toFixed(2)} · ${h.load15.toFixed(2)}`;
    document.getElementById("m-cpu").textContent = pct(h.cpuPct);
    setBar("m-cpu-bar", h.cpuPct);
    document.getElementById("m-mem").textContent =
      `${fmtBytes(h.memUsed)} / ${fmtBytes(h.memTotal)}`;
    setBar("m-mem-bar", ratio(h.memUsed, h.memTotal));
    document.getElementById("m-disk").textContent =
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
      const cell = (text) => {
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
function watchWorkloadMetrics() {
  const el = document.getElementById("workload-metrics");
  if (!el) return;

  const es = new EventSource("/events/workloads/" + el.dataset.name + "/metrics");
  es.addEventListener("sample", (ev) => {
    const c = JSON.parse(ev.data);
    document.getElementById("wm-cpu").textContent = c.running ? fmtPct(c.cpuPct) : "—";
    document.getElementById("wm-mem").textContent = c.running ? fmtBytes(c.memBytes) : "—";
    document.getElementById("wm-tasks").textContent = c.running ? String(c.tasks) : "—";
  });
  es.onerror = () => {};
}

document.addEventListener("DOMContentLoaded", watchWorkloadMetrics);

// Tabbed views on the workload page. The status header and action bar stay
// above the strip; only the lower region (Config/Logs/Terminal/Revisions)
// swaps. Panels stay in the DOM (toggled via `hidden`) so their SSE and
// terminal WebSocket streams keep running when a tab is inactive — the
// terminal's own ResizeObserver refits it when its panel is revealed.
function initWorkloadTabs() {
  const tabs = document.getElementById("workload-tabs");
  if (!tabs) return;
  const tablist = tabs.querySelector(".tablist");
  const buttons = Array.from(tabs.querySelectorAll(".tab"));
  if (!buttons.length) return;

  // Remembered per page path so a full reload after Start/Stop/Apply (those
  // POST → redirect back and drop any URL hash) lands on the same tab.
  const key = "nixbox-tab:" + location.pathname;

  function activate(name, { focus = false, persist = true } = {}) {
    const btn = buttons.find((b) => b.dataset.tab === name);
    if (!btn) return;
    for (const b of buttons) {
      const on = b === btn;
      b.setAttribute("aria-selected", on ? "true" : "false");
      b.tabIndex = on ? 0 : -1;
      const panel = document.getElementById(b.getAttribute("aria-controls"));
      if (panel) panel.hidden = !on;
    }
    if (focus) btn.focus();
    if (persist) {
      sessionStorage.setItem(key, name);
      history.replaceState(null, "", "#" + name);
    }
  }

  tablist.addEventListener("click", (ev) => {
    const btn = ev.target.closest(".tab");
    if (btn) activate(btn.dataset.tab, { focus: true });
  });

  // Roving-tabindex keyboard navigation across the strip (WAI-ARIA tabs).
  tablist.addEventListener("keydown", (ev) => {
    const i = buttons.indexOf(document.activeElement);
    if (i < 0) return;
    let j = null;
    if (ev.key === "ArrowRight") j = (i + 1) % buttons.length;
    else if (ev.key === "ArrowLeft") j = (i - 1 + buttons.length) % buttons.length;
    else if (ev.key === "Home") j = 0;
    else if (ev.key === "End") j = buttons.length - 1;
    if (j === null) return;
    ev.preventDefault();
    activate(buttons[j].dataset.tab, { focus: true });
  });

  // Restore on load: a URL hash wins (deep links), else the remembered tab.
  const wanted = location.hash.slice(1) || sessionStorage.getItem(key);
  if (wanted && buttons.some((b) => b.dataset.tab === wanted)) {
    activate(wanted, { persist: false });
  }

  // Apply / Dry build / Destroy render their live log into #job-panel, which
  // lives on the Config panel. Fired from the always-visible header, that can
  // happen while another tab is active — surface the log so it isn't missed.
  document.body.addEventListener("htmx:afterSwap", (ev) => {
    if (ev.target?.id === "job-panel" && ev.target.firstElementChild) activate("config");
  });
}

document.addEventListener("DOMContentLoaded", initWorkloadTabs);

// Host ports are edited as repeatable rows inside the save form. Add/
// remove clone or drop a row; each structural change nudges the unsaved-
// changes guard so Apply/Dry build stay disabled until the row is saved.
function initPortEditor() {
  const container = document.getElementById("host-ports");
  if (!container) return;
  const tpl = document.getElementById("port-row-tpl");
  const form = container.closest("form");

  function notify() {
    form?.dispatchEvent(new Event("input", { bubbles: true }));
  }

  document.querySelector(".port-add")?.addEventListener("click", () => {
    container.appendChild(tpl.content.cloneNode(true));
    container.querySelector(".port-row:last-child input")?.focus();
    notify();
  });

  container.addEventListener("click", (ev) => {
    const del = ev.target.closest(".port-del");
    if (!del) return;
    del.closest(".port-row")?.remove();
    notify();
  });
}

document.addEventListener("DOMContentLoaded", initPortEditor);

// Apply and Dry build always rebuild from the last *saved* file (and the
// saved host ports), so unsaved edits would silently not be deployed.
// Disable those buttons whenever the save form differs from its last
// save. A form signature covers the editor textarea and every port row.
function guardUnsavedEditor() {
  const ta = document.querySelector("textarea.editor");
  if (!ta || !ta.form) return;
  const form = ta.form;
  const guarded = Array.from(document.querySelectorAll("[data-requires-saved]"))
    .map((btn) => ({ btn, wasDisabled: btn.disabled }));

  const signature = () => new URLSearchParams(new FormData(form)).toString();
  let savedValue = signature();
  let inflightValue = null;

  function refresh() {
    const dirty = signature() !== savedValue;
    for (const { btn, wasDisabled } of guarded) {
      btn.disabled = wasDisabled || dirty;
      btn.title = !wasDisabled && dirty ? msg("msgUnsaved") : "";
    }
  }

  form.addEventListener("input", refresh);
  form.addEventListener("change", refresh);

  // A completed save makes the value sent (not the possibly newer
  // current one) the new baseline. Saves are issued by the form
  // itself; requests from buttons inside it (Dry build) don't count.
  form.addEventListener("htmx:beforeRequest", (ev) => {
    if (ev.detail.elt === form) inflightValue = signature();
  });
  form.addEventListener("htmx:afterRequest", (ev) => {
    if (ev.detail.elt === form && ev.detail.successful && inflightValue !== null) {
      savedValue = inflightValue;
      inflightValue = null;
      refresh();
    }
  });

  refresh();
}

document.addEventListener("DOMContentLoaded", guardUnsavedEditor);

// Native <dialog> modals: [data-open-dialog="id"] opens that dialog,
// [data-close-dialog] closes the enclosing one, and a form marked
// [data-close-on-success] closes its dialog after a successful submit
// (e.g. destroy hands off to the job panel underneath).
document.addEventListener("click", (ev) => {
  if (!(ev.target instanceof Element)) return;

  const opener = ev.target.closest("[data-open-dialog]");
  if (opener) {
    const dlg = document.getElementById(opener.dataset.openDialog);
    if (dlg instanceof HTMLDialogElement) dlg.showModal();
    return;
  }

  const closer = ev.target.closest("[data-close-dialog]");
  if (closer) closer.closest("dialog")?.close();
});

document.addEventListener("htmx:afterRequest", (ev) => {
  const form = ev.target;
  if (!(form instanceof Element) || !form.matches("[data-close-on-success]")) return;
  if (ev.detail.successful) form.closest("dialog")?.close();
});

// Theme toggle: an explicit light/dark choice is stored in localStorage and
// mirrored on <html data-theme>; with nothing stored, CSS follows the OS.
document.addEventListener("click", (ev) => {
  if (!(ev.target instanceof Element) || !ev.target.closest(".theme-toggle")) return;
  const chosen = document.documentElement.dataset.theme;
  const system = matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  const next = (chosen || system) === "dark" ? "light" : "dark";
  document.documentElement.dataset.theme = next;
  localStorage.setItem("nixbox-theme", next);
});

// Editor niceties: Tab inserts two spaces, Ctrl+S submits the save form.
document.addEventListener("keydown", (ev) => {
  const ta = ev.target;
  if (!(ta instanceof HTMLTextAreaElement) || !ta.classList.contains("editor")) return;

  if (ev.key === "Tab") {
    ev.preventDefault();
    const start = ta.selectionStart;
    ta.setRangeText("  ", start, ta.selectionEnd, "end");
  } else if (ev.key === "s" && (ev.ctrlKey || ev.metaKey)) {
    ev.preventDefault();
    ta.form?.requestSubmit();
  }
});
