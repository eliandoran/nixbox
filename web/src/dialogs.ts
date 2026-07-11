// Native <dialog> modals: [data-open-dialog="id"] opens that dialog,
// [data-close-dialog] closes the enclosing one, and a form marked
// [data-close-on-success] closes its dialog after a successful submit
// (e.g. destroy hands off to the job panel underneath).
document.addEventListener("click", (ev) => {
  if (!(ev.target instanceof Element)) return;

  const opener = ev.target.closest<HTMLElement>("[data-open-dialog]");
  if (opener) {
    const dlg = document.getElementById(opener.dataset.openDialog!);
    if (dlg instanceof HTMLDialogElement) dlg.showModal();
    return;
  }

  const closer = ev.target.closest("[data-close-dialog]");
  if (closer) {
    // Cancel is an <a href> for the no-JS full page; inside an open dialog,
    // suppress that navigation and just close the modal.
    const dlg = closer.closest("dialog");
    if (dlg?.open) {
      ev.preventDefault();
      dlg.close();
    }
  }
});

document.addEventListener("htmx:afterRequest", (ev) => {
  const form = ev.target;
  if (!(form instanceof Element) || !form.matches("[data-close-on-success]")) return;
  if ((ev as CustomEvent).detail.successful) form.closest("dialog")?.close();
});

// Lazy modals: a trigger hx-gets a fragment into a [data-lazy] dialog's
// slot; open the dialog once the fragment lands. Nested swaps inside the
// already-open dialog (e.g. the new-workload type picker) are ignored by
// the open guard, so this only fires on the initial fill.
document.addEventListener("htmx:afterSwap", (ev) => {
  if (!(ev.target instanceof Element)) return;
  const dlg = ev.target.closest("dialog.modal[data-lazy]");
  if (dlg instanceof HTMLDialogElement && !dlg.open) dlg.showModal();
});

// Form re-renders on validation failure come back as 422 (the correct
// status), which htmx would otherwise treat as an error and not swap. Opt
// those in so the re-rendered form (with its error banner) replaces the old.
document.addEventListener("htmx:beforeSwap", (ev) => {
  const detail = (ev as CustomEvent).detail;
  if (detail.xhr.status === 422) detail.shouldSwap = true;
});

// Unified confirmation: a form marked [data-confirm="message"] pops the
// shared #confirm-dialog instead of the browser's confirm(). Submitting is
// held until the user confirms, then the same form is resubmitted (the
// data-confirmed flag lets that second submit through). Cancel does nothing.
//
// Registered in the capture phase and stopping propagation so it also gates
// hx-post forms: htmx's own submit handler sits on the form (target phase),
// which capture runs ahead of — otherwise the request would fire unconfirmed.
let pendingConfirm: HTMLFormElement | null = null;
document.addEventListener("submit", (ev) => {
  const form = ev.target;
  if (!(form instanceof HTMLFormElement) || !form.dataset.confirm) return;
  if (form.dataset.confirmed) {
    delete form.dataset.confirmed;
    return; // already confirmed — let this submit proceed to htmx / navigation
  }
  ev.preventDefault();
  ev.stopPropagation();
  pendingConfirm = form;
  document.getElementById("confirm-message")!.textContent = form.dataset.confirm;
  (document.getElementById("confirm-dialog") as HTMLDialogElement).showModal();
}, true);

document.addEventListener("click", (ev) => {
  if (!(ev.target instanceof Element) || !ev.target.closest("[data-confirm-ok]")) return;
  ev.target.closest("dialog")?.close();
  const form = pendingConfirm;
  pendingConfirm = null;
  if (form) {
    form.dataset.confirmed = "1";
    form.requestSubmit();
  }
});

// Secrets: one dialog serves both Add and Edit. The Add button opens it
// empty; each row's Edit button carries the secret's identity + mounts in
// data-* attributes, and clicking it fills the form and re-points it at the
// per-secret save endpoint. Both roles submit as a normal form (server
// redirects back), so nothing here handles the response.
function initSecretDialog(): void {
  const dlg = document.getElementById("secret-dialog") as HTMLDialogElement | null;
  if (!dlg) return;
  const form = document.getElementById("secret-form") as HTMLFormElement;
  const title = document.getElementById("secret-dialog-title")!;
  const submit = document.getElementById("secret-submit")!;
  const nameInput = form.elements.namedItem("name") as HTMLInputElement;
  const value = form.elements.namedItem("value") as HTMLInputElement;
  const mounts = form.querySelectorAll<HTMLInputElement>('input[name="mount"]');

  const setMounts = (ids: string[]) => {
    const set = new Set(ids);
    mounts.forEach((cb) => { cb.checked = set.has(cb.value); });
  };

  document.addEventListener("click", (ev) => {
    if (!(ev.target instanceof Element)) return;

    if (ev.target.closest("[data-add-secret]")) {
      form.reset();
      form.action = "/secrets";
      title.textContent = dlg.dataset.titleAdd!;
      submit.textContent = dlg.dataset.submitAdd!;
      nameInput.disabled = false;
      nameInput.required = true;
      value.required = true;
      value.placeholder = "";
      setMounts([]);
      dlg.showModal();
      nameInput.focus();
      return;
    }

    const edit = ev.target.closest<HTMLElement>("[data-edit-secret]");
    if (edit) {
      const d = edit.dataset;
      form.reset();
      form.action = "/secrets/" + encodeURIComponent(d.editSecret!) + "/save";
      title.textContent = dlg.dataset.titleEdit!;
      submit.textContent = dlg.dataset.submitEdit!;
      // Name is the immutable key; show it read-only. A disabled field is
      // not submitted, which is what the save endpoint wants (it reads the
      // name from the URL).
      nameInput.value = d.editSecret!;
      nameInput.disabled = true;
      nameInput.required = false;
      // Blank keeps the current ciphertext; the plaintext is never shown back.
      value.required = false;
      value.placeholder = dlg.dataset.valueKeep!;
      (form.elements.namedItem("owner") as HTMLInputElement).value = d.owner!;
      (form.elements.namedItem("group") as HTMLInputElement).value = d.group!;
      (form.elements.namedItem("mode") as HTMLInputElement).value = d.mode!;
      setMounts(d.mounts ? d.mounts.split(",") : []);
      dlg.showModal();
      value.focus();
    }
  });
}
document.addEventListener("DOMContentLoaded", initSecretDialog);

// Flake inputs: same double-role dialog pattern as secrets. Add opens it
// empty; each row's Edit button carries the input's ref + follows flag in
// data-* attributes, fills the form, and re-points it at the per-input save
// endpoint. Both roles submit as a normal form (server redirects back).
function initFlakeDialog(): void {
  const dlg = document.getElementById("flake-dialog") as HTMLDialogElement | null;
  if (!dlg) return;
  const form = document.getElementById("flake-form") as HTMLFormElement;
  const title = document.getElementById("flake-dialog-title")!;
  const submit = document.getElementById("flake-submit")!;
  const nameInput = form.elements.namedItem("name") as HTMLInputElement;
  const follows = form.elements.namedItem("follows_nixpkgs") as HTMLInputElement;
  const url = form.elements.namedItem("url") as HTMLInputElement;

  document.addEventListener("click", (ev) => {
    if (!(ev.target instanceof Element)) return;

    if (ev.target.closest("[data-add-input]")) {
      form.reset();
      form.action = "/flakes";
      title.textContent = dlg.dataset.titleAdd!;
      submit.textContent = dlg.dataset.submitAdd!;
      nameInput.disabled = false;
      nameInput.required = true;
      dlg.showModal();
      nameInput.focus();
      return;
    }

    const edit = ev.target.closest<HTMLElement>("[data-edit-input]");
    if (edit) {
      const d = edit.dataset;
      form.reset();
      form.action = "/flakes/" + encodeURIComponent(d.editInput!) + "/save";
      title.textContent = dlg.dataset.titleEdit!;
      submit.textContent = dlg.dataset.submitEdit!;
      // Name is the immutable key; show it read-only (disabled → not
      // submitted, and the save endpoint reads the name from the URL).
      nameInput.value = d.editInput!;
      nameInput.disabled = true;
      nameInput.required = false;
      url.value = d.ref!;
      follows.checked = d.follows === "1";
      dlg.showModal();
      url.focus();
    }
  });
}
document.addEventListener("DOMContentLoaded", initFlakeDialog);
