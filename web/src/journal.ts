// Container journal: follow the SSE journalctl stream into
// #container-log; the checkbox switches between the host-side unit
// journal and the journal from inside the container.
function watchContainerLogs(): void {
  const pre = document.getElementById("container-log");
  if (!pre) return;
  const toggle = document.getElementById("log-source-inside") as HTMLInputElement | null;
  let es: EventSource | undefined;

  // An arrow (created after the null guard above) rather than a hoisted
  // declaration, so the narrowing of `pre` flows into the closure.
  const connect = () => {
    if (es) es.close();
    pre.textContent = "";
    const source = toggle?.checked ? "?source=container" : "";
    es = new EventSource("/workloads/" + pre.dataset.name + "/logs" + source);
    es.addEventListener("append", (ev) => {
      const follow = pre.scrollHeight - pre.scrollTop - pre.clientHeight < 40;
      pre.textContent += (ev as MessageEvent).data + "\n";
      if (follow) pre.scrollTop = pre.scrollHeight;
    });
  };

  toggle?.addEventListener("change", connect);
  connect();
}

document.addEventListener("DOMContentLoaded", watchContainerLogs);
