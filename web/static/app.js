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
