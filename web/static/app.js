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
      btn.title = !wasDisabled && dirty ? "Unsaved changes — save first" : "";
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
