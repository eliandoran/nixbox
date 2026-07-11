// Language picker: picking a locale submits the topbar form, which sets
// the nixbox-lang cookie and redirects back (a full reload re-renders
// everything in the new language). Works without JS via Enter-to-submit.
document.addEventListener("change", (ev) => {
  if (ev.target instanceof HTMLSelectElement && ev.target.matches(".lang-select")) {
    ev.target.form?.requestSubmit();
  }
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
