// Live job log: tail the job's SSE stream into the #job-log pre. Runs
// on initial page load (system page with an active job) and again
// whenever HTMX swaps the job-log fragment in. The afterSwap hook below
// replaces the inline <script> the fragment used to carry — which also
// ran too early on full page loads, before this deferred bundle had
// defined watchJob.
function watchJob(): void {
  const pre = document.getElementById("job-log");
  if (!pre || pre.dataset.watching) return;
  pre.dataset.watching = "1";

  const id = pre.dataset.jobId;
  const es = new EventSource("/events/jobs/" + id);

  es.addEventListener("append", (ev) => {
    pre.textContent += (ev as MessageEvent).data + "\n";
    pre.scrollTop = pre.scrollHeight;
  });

  es.addEventListener("done", (ev) => {
    es.close();
    const badge = document.getElementById("job-status");
    if (badge) {
      badge.textContent = (ev as MessageEvent).data;
      badge.className = "status status-" + (ev as MessageEvent).data;
    }
  });

  es.onerror = () => es.close();
}

document.addEventListener("DOMContentLoaded", watchJob);
// watchJob's own not-already-watching guard makes the unconditional
// call idempotent, so every swap can safely probe for a fresh #job-log.
document.addEventListener("htmx:afterSwap", () => watchJob());
