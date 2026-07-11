// Tabbed views on the workload page. The status header and action bar stay
// above the strip; only the lower region (Config/Logs/Terminal/Revisions)
// swaps. Panels stay in the DOM (toggled via `hidden`) so their SSE and
// terminal WebSocket streams keep running when a tab is inactive — the
// terminal's own ResizeObserver refits it when its panel is revealed.
function initWorkloadTabs(): void {
  const tabs = document.getElementById("workload-tabs");
  if (!tabs) return;
  const tablist = tabs.querySelector<HTMLElement>(".tablist")!;
  const buttons = Array.from(tabs.querySelectorAll<HTMLButtonElement>(".tab"));
  if (!buttons.length) return;

  // Remembered per page path so a full reload after Start/Stop/Apply (those
  // POST → redirect back and drop any URL hash) lands on the same tab.
  const key = "nixbox-tab:" + location.pathname;

  function activate(name: string, { focus = false, persist = true } = {}) {
    const btn = buttons.find((b) => b.dataset.tab === name);
    if (!btn) return;
    for (const b of buttons) {
      const on = b === btn;
      b.setAttribute("aria-selected", on ? "true" : "false");
      b.tabIndex = on ? 0 : -1;
      const panel = document.getElementById(b.getAttribute("aria-controls")!);
      if (panel) panel.hidden = !on;
    }
    if (focus) btn.focus();
    if (persist) {
      sessionStorage.setItem(key, name);
      history.replaceState(null, "", "#" + name);
    }
  }

  tablist.addEventListener("click", (ev) => {
    const btn = (ev.target as Element).closest<HTMLElement>(".tab");
    if (btn) activate(btn.dataset.tab!, { focus: true });
  });

  // Roving-tabindex keyboard navigation across the strip (WAI-ARIA tabs).
  tablist.addEventListener("keydown", (ev) => {
    const i = buttons.indexOf(document.activeElement as HTMLButtonElement);
    if (i < 0) return;
    let j: number | null = null;
    if (ev.key === "ArrowRight") j = (i + 1) % buttons.length;
    else if (ev.key === "ArrowLeft") j = (i - 1 + buttons.length) % buttons.length;
    else if (ev.key === "Home") j = 0;
    else if (ev.key === "End") j = buttons.length - 1;
    if (j === null) return;
    ev.preventDefault();
    activate(buttons[j].dataset.tab!, { focus: true });
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
    const t = ev.target as Element | null;
    if (t?.id === "job-panel" && t.firstElementChild) activate("config");
  });
}

document.addEventListener("DOMContentLoaded", initWorkloadTabs);
