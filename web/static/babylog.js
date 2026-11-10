// Baby log: keeps the "since the last feed" clock current, and queues entries
// on the phone when there is no connection, sending them when it returns.
// Each entry carries a client id, so a retry never logs twice.
(() => {
  "use strict";
  const KEY = "mithra.careOutbox.v1";
  const tick = (hero) => {
    const started = Date.parse(hero.dataset.since);
    const hours = hero.querySelector("[data-since-hours]");
    const mins = hero.querySelector("[data-since-minutes]");
    if (Number.isNaN(started) || !hours || !mins) return;
    const minutes = Math.max(0, Math.floor((Date.now() - started) / 60000));
    hours.textContent = String(Math.floor(minutes / 60));
    mins.textContent = String(minutes % 60);
  };
  const clocks = document.querySelectorAll("[data-since]");
  if (clocks.length > 0) {
    clocks.forEach(tick);
    setInterval(() => clocks.forEach(tick), 30000);
  }

  const log = document.querySelector("[data-baby-log]");
  if (!log) return;

  // After a post, bring the message about it into view and announce it.
  const message = log.querySelector("[data-focus-on-load]");
  if (message) {
    message.focus({ preventScroll: true });
    message.scrollIntoView({ block: "center" });
  }

  // Check a bottle before it is sent: an amount is needed, and taken
  // cannot be more than offered.
  log.querySelectorAll("form[data-bottle-form]").forEach((form) => {
    const offered = form.querySelector("[data-offered]");
    const taken = form.querySelector("[data-taken]");
    if (!offered || !taken) return;
    const check = () => {
      let problem = "";
      if (offered.value === "" && taken.value === "") problem = "Enter how much was offered or taken, in ml.";
      else if (offered.value !== "" && taken.value !== "" && Number(taken.value) > Number(offered.value)) problem = "Taken can’t be more than offered.";
      taken.setCustomValidity(problem);
      if (problem) taken.setAttribute("aria-invalid", "true"); else taken.removeAttribute("aria-invalid");
    };
    offered.addEventListener("input", check);
    taken.addEventListener("input", check);
    form.addEventListener("submit", (event) => {
      check();
      if (!form.checkValidity()) { event.preventDefault(); event.stopImmediatePropagation(); taken.reportValidity(); }
    }, true);
  });

  const read = () => {
    try { return JSON.parse(localStorage.getItem(KEY) || "[]"); } catch { return []; }
  };
  const write = (items) => {
    try { localStorage.setItem(KEY, JSON.stringify(items)); return true; } catch { return false; }
  };
  const status = log.querySelector("[data-outbox-status]");
  const show = () => {
    const count = read().length;
    if (!status) return;
    status.hidden = count === 0;
    status.textContent = count === 1 ? "1 unsent" : count + " unsent";
  };
  const newID = () => (crypto.randomUUID ? crypto.randomUUID() : String(Date.now()) + Math.random().toString(16).slice(2));

  log.querySelectorAll("form[data-care-form]").forEach((form) => {
    form.addEventListener("submit", (event) => {
      const id = form.querySelector('input[name="client_id"]');
      const at = form.querySelector('input[name="logged_at"]');
      if (id) id.value = newID();
      if (at) at.value = new Date().toISOString();
      if (navigator.onLine !== false) return;
      event.preventDefault();
      const body = new URLSearchParams(new FormData(form, event.submitter || undefined)).toString();
      const items = read();
      items.push({ url: new URL(form.getAttribute("action"), location.href).href, body });
      if (!write(items)) return;
      show();
      form.reset();
    });
  });

  let sending = false;
  const flush = async () => {
    if (sending || navigator.onLine === false) return;
    const items = read();
    if (items.length === 0) return;
    sending = true;
    let sent = 0;
    while (items.length > 0) {
      const item = items[0];
      let response;
      try {
        response = await fetch(item.url, { method: "POST", body: item.body, credentials: "same-origin", headers: { "Content-Type": "application/x-www-form-urlencoded" } });
      } catch {
        break;
      }
      // Keep an entry the server could not take for now; drop one it refused as invalid.
      if (response.status >= 500 || response.status === 403) break;
      items.shift();
      write(items);
      sent++;
    }
    sending = false;
    show();
    if (sent > 0 && items.length === 0) location.reload();
  };

  show();
  window.addEventListener("online", flush);
  flush();
})();
