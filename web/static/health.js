(() => {
  "use strict";
  const form = document.querySelector("[data-auto-submit]");
  const select = form?.querySelector("select");
  if (!form || !select) return;
  form.classList.add("is-enhanced");
  select.addEventListener("change", () => form.requestSubmit());
})();
