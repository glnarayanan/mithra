// Puts the baby log at the top of the Add sheet: start or finish a feed and
// log a diaper in one tap, for each young child in the household.
(() => {
  "use strict";
  const sheet = document.getElementById("quick-capture");
  const mount = document.querySelector("[data-quick-baby]");
  if (!sheet || !mount) return;
  const csrfInput = sheet.querySelector('input[name="csrf"]');
  let loaded = false;

  const post = (url, fields) => {
    const form = document.createElement("form");
    form.method = "post";
    form.action = url;
    const all = Object.assign({ csrf: csrfInput ? csrfInput.value : "" }, fields);
    Object.keys(all).forEach((name) => {
      const input = document.createElement("input");
      input.type = "hidden";
      input.name = name;
      input.value = all[name];
      form.appendChild(input);
    });
    document.body.appendChild(form);
    form.submit();
  };

  const button = (label, className, onClick) => {
    const b = document.createElement("button");
    b.type = "button";
    b.className = className;
    b.textContent = label;
    b.addEventListener("click", onClick);
    return b;
  };

  const render = (babies) => {
    mount.replaceChildren();
    babies.forEach((baby) => {
      const row = document.createElement("div");
      row.className = "quick-baby-row";
      const head = document.createElement("p");
      head.className = "quick-baby-name";
      const link = document.createElement("a");
      link.href = baby.pageUrl;
      link.textContent = baby.name;
      head.appendChild(link);
      row.appendChild(head);
      const actions = document.createElement("div");
      actions.className = "quick-baby-actions";
      if (baby.running) {
        actions.appendChild(button("Finish feed", "button button-feed", () => post(baby.logUrl, { action: "finish", event: baby.running })));
      } else {
        const side = baby.nextSide === "right" ? "Right" : baby.nextSide === "both" ? "Both sides" : "Left";
        actions.appendChild(button("Start feed, " + side.toLowerCase(), "button button-feed", () => post(baby.logUrl, { action: "feed", mode: "breast", side: baby.nextSide || "left" })));
      }
      actions.appendChild(button("Wet", "button", () => post(baby.logUrl, { action: "diaper", kind: "wet" })));
      actions.appendChild(button("Dirty", "button", () => post(baby.logUrl, { action: "diaper", kind: "dirty" })));
      row.appendChild(actions);
      mount.appendChild(row);
    });
    mount.hidden = babies.length === 0;
  };

  const load = async () => {
    if (loaded) return;
    loaded = true;
    try {
      const response = await fetch("/family/quick", { credentials: "same-origin", headers: { Accept: "application/json" } });
      if (!response.ok) return;
      render(await response.json());
    } catch {
      loaded = false;
    }
  };

  // The sheet opens as a dialog; load the row the first time it shows.
  new MutationObserver(() => { if (sheet.open) load(); }).observe(sheet, { attributes: true, attributeFilter: ["open"] });
})();
