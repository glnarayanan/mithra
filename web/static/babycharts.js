// Baby log charts: daily rhythm, bottle intake, diapers and weight. The
// server counts everything and writes it to data-chart-data; this script
// draws each chart at the width it is shown and answers hover, touch and
// the arrow keys with the numbers for that day or that weight.
(() => {
  "use strict";

  const esc = (value) => String(value).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
  const plural = (n, one, many) => `${n} ${n === 1 ? one : many}`;
  const fixed = (n, digits) => Number(n).toFixed(digits);
  const signed = (value, digits = 0) => {
    const rounded = Number(value.toFixed(digits));
    if (rounded === 0) return (0).toFixed(digits);
    return `${rounded > 0 ? "+" : "−"}${Math.abs(rounded).toFixed(digits)}`;
  };
  const kg = (value) => String(Math.round(value * 1000) / 1000);

  // A transparent band that answers the pointer and the keys with a tooltip.
  const hit = (x, y, w, h, title, lines) => `<rect class="hit" x="${x.toFixed(1)}" y="${y.toFixed(1)}" width="${Math.max(1, w).toFixed(1)}" height="${h.toFixed(1)}" data-tip="${esc(title)}" data-lines="${esc(lines.join("\n"))}"/>`;

  const table = (caption, head, rows) => `<table class="visually-hidden"><caption>${esc(caption)}</caption><thead><tr>${head.map((c) => `<th scope="col">${esc(c)}</th>`).join("")}</tr></thead><tbody>${rows.map((row) => `<tr>${row.map((c, i) => (i ? `<td>${esc(c)}</td>` : `<th scope="row">${esc(c)}</th>`)).join("")}</tr>`).join("")}</tbody></table>`;

  const dayLabel = (day, days) => (days.length > 14 ? day.num : day.short);

  function perDay(data, test) {
    const counts = data.days.map(() => 0);
    data.events.forEach((e) => { if (test(e)) counts[e.d] += 1; });
    return counts;
  }

  // Columns per day with day labels under them. Labels thin out on narrow
  // slots so nothing collides.
  function bars(W, data, series, label, tip) {
    const days = data.days;
    const height = 180;
    const top = 22;
    const base = height - 26;
    const max = Math.max(1, ...series.flatMap((s) => s.values));
    const slot = W / days.length;
    const group = Math.min(36, slot * 0.7);
    const bar = group / series.length;
    const step = Math.max(1, Math.ceil(44 / slot));
    let marks = "";
    let hits = "";
    days.forEach((day, i) => {
      hits += hit(i * slot, top - 14, slot, base - top + 14, day.long, tip(i));
      const x0 = i * slot + (slot - group) / 2;
      series.forEach((s, si) => {
        const value = s.values[i];
        const h = value ? Math.max(3, ((base - top) * value) / max) : 0;
        if (h) marks += `<rect class="cm ${s.cls}" x="${(x0 + si * bar).toFixed(1)}" y="${(base - h).toFixed(1)}" width="${Math.max(2, bar - 2).toFixed(1)}" height="${h.toFixed(1)}" rx="${Math.min(6, Math.max(2, bar / 4)).toFixed(1)}"/>`;
        if (value && days.length <= 7 && series.length === 1) marks += `<text class="chart-value" x="${(x0 + group / 2).toFixed(1)}" y="${(base - h - 6).toFixed(1)}" text-anchor="middle">${Math.round(value)}</text>`;
      });
      if (i % step === 0 || i === days.length - 1) marks += `<text class="chart-label" x="${(i * slot + slot / 2).toFixed(1)}" y="${height - 6}" text-anchor="middle">${esc(dayLabel(day, days))}</text>`;
    });
    return `<svg class="chart" viewBox="0 0 ${W} ${height}" width="${W}" height="${height}" role="img" aria-label="${esc(label)}">${hits}<line class="axis" x1="0" x2="${W}" y1="${base}" y2="${base}"/>${marks}</svg>`;
  }

  // The entry types' shapes: feed circle, diaper rounded square, spit-up drop.
  function shape(type, x, y, r) {
    if (type === "diaper") return `<rect class="cm diaper" x="${(x - r).toFixed(1)}" y="${(y - r).toFixed(1)}" width="${(r * 2).toFixed(1)}" height="${(r * 2).toFixed(1)}" rx="${(r * 0.5).toFixed(1)}"/>`;
    if (type === "spitup") return `<path class="cm spitup" d="M${x.toFixed(1)} ${(y - r * 1.25).toFixed(1)}C${(x + r * 0.9).toFixed(1)} ${(y - r * 0.2).toFixed(1)} ${(x + r).toFixed(1)} ${(y + r * 0.2).toFixed(1)} ${(x + r).toFixed(1)} ${(y + r * 0.25).toFixed(1)}A${r.toFixed(1)} ${r.toFixed(1)} 0 0 1 ${(x - r).toFixed(1)} ${(y + r * 0.25).toFixed(1)}C${(x - r).toFixed(1)} ${(y + r * 0.2).toFixed(1)} ${(x - r * 0.9).toFixed(1)} ${(y - r * 0.2).toFixed(1)} ${x.toFixed(1)} ${(y - r * 1.25).toFixed(1)}Z"/>`;
    return `<circle class="cm feed" cx="${x.toFixed(1)}" cy="${y.toFixed(1)}" r="${r}"/>`;
  }

  function rhythm(W, data) {
    const days = data.days;
    const n = days.length;
    const row = n > 31 ? 6 : n > 14 ? 9 : n > 7 ? 14 : 22;
    const left = 44;
    const height = n * row + 30;
    const feeds = perDay(data, (e) => e.t === "feed");
    const diapers = perDay(data, (e) => e.t === "diaper");
    const spits = perDay(data, (e) => e.t === "spitup");
    let grid = "";
    let hits = "";
    days.forEach((day, i) => {
      const y = i * row + row / 2;
      if (n <= 7 || i % (n > 31 ? 10 : 5) === 0 || i === n - 1) grid += `<text class="chart-label" x="0" y="${(y + 4).toFixed(1)}">${esc(dayLabel(day, days))}</text>`;
      grid += `<line class="grid" x1="${left}" x2="${W}" y1="${y.toFixed(1)}" y2="${y.toFixed(1)}"/>`;
      hits += hit(left, i * row, W - left, row, day.long, [plural(feeds[i], "feed", "feeds"), plural(diapers[i], "diaper", "diapers"), plural(spits[i], "spit-up", "spit-ups")]);
    });
    const hours = [0, 6, 12, 18, 24].map((h) => {
      const x = left + ((W - left) * h) / 24;
      return `<text class="chart-label" x="${x.toFixed(1)}" y="${height - 6}" text-anchor="${h === 0 ? "start" : h === 24 ? "end" : "middle"}">${h === 24 ? "24h" : `${h}h`}</text>`;
    }).join("");
    const r = n > 31 ? 2.2 : n > 14 ? 3 : 4.5;
    const dots = data.events.map((e) => shape(e.t, left + ((W - left) * e.m) / 1440, e.d * row + row / 2, r)).join("");
    return `<svg class="chart" viewBox="0 0 ${W} ${height}" width="${W}" height="${height}" role="img" aria-label="Feeds, diapers and spit-ups by time of day, one row per day">${hits}${grid}${hours}${dots}</svg>` +
      table("Entries per day", ["Day", "Feeds", "Diapers", "Spit-ups"], days.map((d, i) => [d.long, feeds[i], diapers[i], spits[i]]));
  }

  function bottle(W, data) {
    const ml = data.days.map(() => 0);
    const count = data.days.map(() => 0);
    data.events.forEach((e) => { if (e.ml) { ml[e.d] += e.ml; count[e.d] += 1; } });
    return bars(W, data, [{ cls: "feed", values: ml }], "Bottle intake per day, ml", (i) => [`${Math.round(ml[i])} ml`, plural(count[i], "bottle", "bottles")]) +
      table("Bottle intake in ml", ["Day", "ml", "Bottles"], data.days.map((d, i) => [d.long, Math.round(ml[i]), count[i]]));
  }

  function diapers(W, data) {
    const wet = perDay(data, (e) => e.t === "diaper" && (e.k === "wet" || e.k === "both"));
    const dirty = perDay(data, (e) => e.t === "diaper" && (e.k === "dirty" || e.k === "both"));
    const all = perDay(data, (e) => e.t === "diaper");
    return bars(W, data, [{ cls: "diaper", values: wet }, { cls: "dirty", values: dirty }], "Wet and dirty diapers per day", (i) => [plural(all[i], "diaper", "diapers"), `${wet[i]} wet, ${dirty[i]} dirty`]) +
      table("Diapers per day", ["Day", "Wet", "Dirty"], data.days.map((d, i) => [d.long, wet[i], dirty[i]]));
  }

  // Weights on a time axis that spans the whole window, so the slope reads
  // the same as the figures above it.
  function weight(W, data, compact) {
    const entries = data.weights || [];
    if (!entries.length) return "";
    const height = compact ? 150 : 180;
    const left = 44;
    const values = entries.map((e) => e.kg);
    const pad = Math.max(0.05, (Math.max(...values) - Math.min(...values)) * 0.08);
    const min = Math.min(...values) - pad;
    const max = Math.max(...values) + pad;
    const span = Math.max(data.to - data.from, 1);
    const plotTop = 10;
    const plotBottom = height - 30;
    const x = (at) => left + 8 + ((at - data.from) / span) * (W - left - 16);
    const y = (v) => plotBottom - ((v - min) / Math.max(max - min, 0.001)) * (plotBottom - plotTop);
    const grid = [min + pad, (min + max) / 2, max - pad].filter((v, i, all) => i === 0 || Math.abs(v - all[i - 1]) > 0.0005).map((v) => `<line class="grid" x1="${left}" x2="${W}" y1="${y(v).toFixed(1)}" y2="${y(v).toFixed(1)}"/><text class="chart-label" x="0" y="${(y(v) + 4).toFixed(1)}">${fixed(v, 2)}</text>`).join("");
    const xs = entries.map((e) => x(e.at));
    const points = entries.map((e, i) => `${xs[i].toFixed(1)},${y(e.kg).toFixed(1)}`).join(" ");
    const dots = entries.map((e, i) => `<circle class="cm weight" cx="${xs[i].toFixed(1)}" cy="${y(e.kg).toFixed(1)}" r="${entries.length > 40 ? 3 : 4.5}"/>`).join("");
    // Each point owns the band halfway to its neighbours.
    const hits = entries.map((e, i) => {
      const from = i ? (xs[i - 1] + xs[i]) / 2 : left;
      const to = i < entries.length - 1 ? (xs[i] + xs[i + 1]) / 2 : W;
      const lines = [`${kg(e.kg)} kg`];
      if (i) {
        const daysBetween = (e.at - entries[i - 1].at) / 864e5;
        const grams = (e.kg - entries[i - 1].kg) * 1000;
        lines.push(`${signed(grams)} g since the weight before`);
        if (daysBetween >= 0.5) lines.push(`${signed(grams / daysBetween)} g a day over ${plural(Math.round(daysBetween), "day", "days")}`);
      }
      return hit(from, plotTop - 6, to - from, plotBottom - plotTop + 12, e.label, lines);
    }).join("");
    const edges = `<text class="chart-label" x="${left + 8}" y="${height - 6}">${esc(data.fromLabel)}</text><text class="chart-label" x="${W}" y="${height - 6}" text-anchor="end">${esc(data.toLabel)}</text>`;
    return `<svg class="chart" viewBox="0 0 ${W} ${height}" width="${W}" height="${height}" role="img" aria-label="Weight in kilograms, ${plural(entries.length, "weight", "weights")} from ${esc(data.fromLabel)} to ${esc(data.toLabel)}">${hits}${grid}<polyline class="line weight" points="${points}"/>${dots}${edges}</svg>` +
      table("Weights", ["Date", "kg"], entries.map((e) => [e.label, kg(e.kg)]));
  }

  const draw = { rhythm, bottle, diapers, weight };

  function render(wrap) {
    let data;
    try {
      data = JSON.parse(wrap.dataset.chartData || "{}");
    } catch {
      return;
    }
    const kind = wrap.dataset.chart;
    if (!draw[kind]) return;
    const W = Math.max(260, Math.round(wrap.clientWidth || 600));
    wrap.innerHTML = draw[kind](W, data, wrap.hasAttribute("data-compact"));
    wrap.dataset.drawn = String(W);
  }

  // ---- Tooltips --------------------------------------------------------

  function tooltip(wrap) {
    let tip = wrap.querySelector(".chart-tip");
    if (!tip) {
      tip = document.createElement("div");
      tip.className = "chart-tip";
      tip.setAttribute("role", "status");
      tip.hidden = true;
      wrap.append(tip);
    }
    return tip;
  }

  function show(wrap, band) {
    wrap.querySelectorAll(".hit.on").forEach((n) => n.classList.remove("on"));
    band.classList.add("on");
    const tip = tooltip(wrap);
    tip.innerHTML = `<strong>${esc(band.dataset.tip)}</strong>${band.dataset.lines.split("\n").filter(Boolean).map((line) => `<span>${esc(line)}</span>`).join("")}`;
    tip.hidden = false;
    const box = wrap.getBoundingClientRect();
    const rect = band.getBoundingClientRect();
    const width = tip.offsetWidth;
    const center = rect.left + rect.width / 2 - box.left;
    // Sit beside the band, on the side with room, so the mark stays visible.
    let left = center < box.width / 2 ? Math.min(rect.right - box.left + 8, box.width - width) : Math.max(rect.left - box.left - width - 8, 0);
    if (rect.width > box.width / 3) left = Math.min(Math.max(center - width / 2, 0), box.width - width);
    tip.style.left = `${Math.max(0, left)}px`;
    tip.style.top = `${Math.max(0, rect.top - box.top)}px`;
  }

  function hide(wrap) {
    wrap.querySelectorAll(".hit.on").forEach((n) => n.classList.remove("on"));
    const tip = wrap.querySelector(".chart-tip");
    if (tip) tip.hidden = true;
  }

  function wire(wrap) {
    const pick = (event) => {
      const band = event.target.closest && event.target.closest(".hit");
      if (band) show(wrap, band);
    };
    wrap.addEventListener("pointermove", pick);
    wrap.addEventListener("pointerdown", pick);
    wrap.addEventListener("pointerleave", (event) => { if (event.pointerType === "mouse") hide(wrap); });
    wrap.addEventListener("blur", () => hide(wrap));
    wrap.addEventListener("focus", () => {
      const bands = wrap.querySelectorAll(".hit");
      if (bands.length) show(wrap, bands[bands.length - 1]);
    });
    wrap.addEventListener("keydown", (event) => {
      const bands = [...wrap.querySelectorAll(".hit")];
      if (!bands.length || !["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown", "Home", "End", "Escape"].includes(event.key)) return;
      event.preventDefault();
      if (event.key === "Escape") return hide(wrap);
      const current = bands.findIndex((n) => n.classList.contains("on"));
      const forward = event.key === "ArrowRight" || event.key === "ArrowDown";
      let next = event.key === "Home" ? 0 : event.key === "End" ? bands.length - 1 : current < 0 ? bands.length - 1 : current + (forward ? 1 : -1);
      next = Math.min(Math.max(next, 0), bands.length - 1);
      show(wrap, bands[next]);
    });
  }

  const wraps = [...document.querySelectorAll("[data-chart]")];
  wraps.forEach((wrap) => { render(wrap); wire(wrap); });
  // A tap outside a chart puts its tooltip away on touch screens.
  document.addEventListener("pointerdown", (event) => {
    wraps.forEach((wrap) => { if (!wrap.contains(event.target)) hide(wrap); });
  });
  let timer = 0;
  window.addEventListener("resize", () => {
    clearTimeout(timer);
    timer = setTimeout(() => {
      wraps.forEach((wrap) => { if (String(Math.max(260, Math.round(wrap.clientWidth))) !== wrap.dataset.drawn) render(wrap); });
    }, 150);
  });
})();
