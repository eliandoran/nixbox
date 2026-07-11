// New-workload form: the ID is derived (slugified) from the display name
// until the user edits the ID by hand — data-auto on the ID input holds the
// last derived value, so "still auto" is simply value === data-auto (or
// empty; clearing the ID re-enables derivation). The ID input lives outside
// the HTMX-swapped type fields, so its constraints (pattern, max length,
// hint) are retargeted here from the type radios' data attributes.
// Delegated listeners, because the modal fragment loads lazily.
const slugify = (s: string, max: number): string =>
  s.normalize("NFKD").replace(/[\u0300-\u036f]/g, "") // strip diacritics
    .toLowerCase().replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+/, "").slice(0, max).replace(/-+$/, "");

document.addEventListener("input", (ev) => {
  const el = ev.target;
  if (!(el instanceof HTMLInputElement)) return;

  if (el.id === "nw-display_name") {
    const name = document.getElementById("nw-name") as HTMLInputElement | null;
    if (!name || (name.value !== "" && name.value !== name.dataset.auto)) return;
    name.value = name.dataset.auto = slugify(el.value, name.maxLength);
  } else if (el.id === "nw-name") {
    delete el.dataset.auto; // hand-edited (or cleared) — stop deriving
  }
});

document.addEventListener("change", (ev) => {
  const radio = ev.target;
  if (!(radio instanceof HTMLInputElement) || radio.name !== "type" ||
      !radio.dataset.namePattern) return;
  const name = document.getElementById("nw-name") as HTMLInputElement | null;
  if (!name) return;
  name.pattern = radio.dataset.namePattern;
  name.maxLength = +radio.dataset.nameMaxlen!;
  const hint = document.getElementById("nw-name-hint");
  if (hint) hint.textContent = radio.dataset.nameHint!;
  // Re-fit a derived ID to the new type's cap; hand-typed values are left
  // alone (the pattern flags them at submit if too long).
  if (name.value && name.value === name.dataset.auto) {
    name.value = name.dataset.auto = slugify(name.value, name.maxLength);
  }
});
