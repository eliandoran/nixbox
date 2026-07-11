import { msg } from "./util";

// Host ports are edited as repeatable rows inside the save form. Add/
// remove clone or drop a row; each structural change nudges the unsaved-
// changes guard so Apply/Dry build stay disabled until the row is saved.
function initPortEditor(): void {
  const container = document.getElementById("host-ports");
  if (!container) return;
  const tpl = document.getElementById("port-row-tpl") as HTMLTemplateElement;
  const form = container.closest("form");

  function notify() {
    form?.dispatchEvent(new Event("input", { bubbles: true }));
  }

  document.querySelector(".port-add")?.addEventListener("click", () => {
    container.appendChild(tpl.content.cloneNode(true));
    container.querySelector<HTMLInputElement>(".port-row:last-child input")?.focus();
    notify();
  });

  container.addEventListener("click", (ev) => {
    const del = (ev.target as Element).closest(".port-del");
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
function guardUnsavedEditor(): void {
  const ta = document.querySelector<HTMLTextAreaElement>("textarea.editor");
  if (!ta || !ta.form) return;
  const form = ta.form;
  const guarded = Array.from(document.querySelectorAll<HTMLButtonElement>("[data-requires-saved]"))
    .map((btn) => ({ btn, wasDisabled: btn.disabled }));

  // URLSearchParams accepts any pairs iterable (FormData included) at
  // runtime; lib.dom's constructor type just hasn't caught up.
  const signature = () =>
    new URLSearchParams(new FormData(form) as unknown as string[][]).toString();
  let savedValue = signature();
  let inflightValue: string | null = null;

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
    if ((ev as CustomEvent).detail.elt === form) inflightValue = signature();
  });
  form.addEventListener("htmx:afterRequest", (ev) => {
    const detail = (ev as CustomEvent).detail;
    if (detail.elt === form && detail.successful && inflightValue !== null) {
      savedValue = inflightValue;
      inflightValue = null;
      refresh();
    }
  });

  refresh();
}

document.addEventListener("DOMContentLoaded", guardUnsavedEditor);

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
