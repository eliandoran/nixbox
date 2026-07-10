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
