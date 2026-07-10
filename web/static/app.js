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
