// Model picker behavior: type to filter (matches in bold, sections kept, first match active), arrow keys, Home/End,
// Enter picks (the row blinks, the check moves, the trigger shows it), Esc clears the query, then closes. The pointer moves
// the one highlight. Anchored pickers sit under their trigger, right edges aligned, and open scrolled to the current model.
(() => {
  const esc = (s) => s.replace(/[&<>]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;" })[c]);
  const rows = (p) => [...p.querySelectorAll(".pk-row")].filter((r) => !r.hidden && !r.closest("[hidden]") && r.getAttribute("aria-disabled") !== "true");
  const setActive = (p, r, scroll) => {
    p.querySelectorAll(".pk-row.is-active").forEach((x) => x.classList.remove("is-active"));
    const input = p.querySelector("input");
    if (!r) { input?.removeAttribute("aria-activedescendant"); return; }
    r.classList.add("is-active");
    if (r.id) input?.setAttribute("aria-activedescendant", r.id);
    if (scroll) r.scrollIntoView({ block: "nearest" });
  };
  const filter = (p, q) => {
    q = q.trim().toLowerCase();
    for (const g of p.querySelectorAll(".pk-list > [role=group]")) {
      const sec = g.querySelector(".pk-label").textContent.toLowerCase();
      let any = false;
      for (const r of g.querySelectorAll(".pk-row")) {
        const n = r.querySelector(".pk-name"), label = (n.dataset.label ??= n.textContent);
        const hit = !q || `${label} ${r.querySelector(".pk-hint")?.textContent || ""} ${sec}`.toLowerCase().includes(q);
        r.hidden = !hit; any ||= hit;
        const i = q ? label.toLowerCase().indexOf(q) : -1;
        n.innerHTML = i < 0 ? esc(label) : esc(label.slice(0, i)) + "<b>" + esc(label.slice(i, i + q.length)) + "</b>" + esc(label.slice(i + q.length));
      }
      g.hidden = !any;
    }
    const list = p.querySelector(".pk-list");
    let none = p.querySelector(".pk-empty.pk-none");
    const empty = list && ![...list.children].some((g) => !g.hidden);
    if (empty && !none) { none = document.createElement("div"); none.className = "pk-empty pk-none"; list.after(none); }
    if (none) { none.hidden = !empty; none.textContent = `No models match “${q}”`; }
    if (list) list.hidden = empty;
    setActive(p, q ? rows(p).find((r) => r.closest(".pk-list")) : null, true);
  };
  // pick fires mp-pick on the picker (detail: the row); a pick in the effort list shows its value on the Effort row and the trigger.
  const pick = (p, r) => {
    if (r.classList.contains("pk-more")) { if (r.getAttribute("aria-controls")) toggleSub(p, r); return; }
    r.classList.add("is-blink");
    setTimeout(() => {
      r.classList.remove("is-blink");
      const sub = r.closest(".pk-sub");
      (sub || p.querySelector(".pk-list")).querySelectorAll('[aria-selected="true"]').forEach((x) => x.setAttribute("aria-selected", "false"));
      r.setAttribute("aria-selected", "true");
      const name = r.querySelector(".pk-name"), label = name.dataset.label || name.textContent;
      const t = p.id && document.querySelector(`[data-picker="${p.id}"]`);
      if (sub) {
        sub.hidden = true;
        p.querySelector(`[aria-controls="${sub.id}"]`).setAttribute("aria-expanded", "false");
        p.querySelector(`[aria-controls="${sub.id}"] .pk-hint`).textContent = label;
        if (t?.querySelector(".pk-effort")) t.querySelector(".pk-effort").textContent = label;
      } else if (t) { t.firstChild.textContent = label; close(p); }
      p.dispatchEvent(new CustomEvent("mp-pick", { detail: r, bubbles: true }));
    }, 70);
  };
  // toggleSub opens the effort list beside the picker, its first row level with the Effort row (left of it where there is no room).
  const toggleSub = (p, r) => {
    const sub = document.getElementById(r.getAttribute("aria-controls"));
    sub.hidden = !sub.hidden; r.setAttribute("aria-expanded", String(!sub.hidden));
    if (sub.hidden || !sub.offsetParent) return;
    const s = sub.offsetParent.getBoundingClientRect(), b = p.getBoundingClientRect(), rr = r.getBoundingClientRect();
    const right = b.right + sub.offsetWidth - 3 <= innerWidth - 8;
    sub.style.left = (right ? b.right - 3 : Math.max(8, b.left - sub.offsetWidth + 3)) - s.left + "px";
    sub.style.top = Math.min(rr.top - 6, innerHeight - sub.offsetHeight - 8) - s.top + "px";
  };
  const close = (p) => { const t = document.querySelector(`[data-picker="${p.id}"]`); if (!t) return; p.hidden = true; p.querySelectorAll(".pk-more[aria-controls]").forEach((r) => { r.setAttribute("aria-expanded", "false"); document.getElementById(r.getAttribute("aria-controls")).hidden = true; }); t.setAttribute("aria-expanded", "false"); t.classList.remove("is-open"); t.focus(); };
  const place = (p, t) => {
    const s = p.offsetParent.getBoundingClientRect(), b = t.getBoundingClientRect();
    p.style.top = b.bottom - s.top + 4 + "px";
    p.style.left = Math.max(8, b.right - s.left - p.offsetWidth + 2) + "px";
  };
  const open = (p, t) => {
    p.hidden = false; t.setAttribute("aria-expanded", "true"); t.classList.add("is-open");
    const input = p.querySelector("input"); input.value = ""; filter(p, "");
    place(p, t);
    const cur = p.querySelector('.pk-list [aria-selected="true"]');
    if (cur) { const l = p.querySelector(".pk-list"); l.scrollTop = cur.offsetTop - l.clientHeight / 2 + cur.offsetHeight / 2; setActive(p, cur); }
    input.focus({ preventScroll: true });
  };
  // init sets a picker up; one made before it is in the page is given its trigger and effort list.
  const more = (p) => p.querySelector(".pk-more[aria-controls]");
  const init = (p, t = p.id && document.querySelector(`[data-picker="${p.id}"]`), sub = more(p) && document.getElementById(more(p).getAttribute("aria-controls"))) => {
    const input = p.querySelector("input");
    input?.addEventListener("input", () => filter(p, input.value));
    input?.addEventListener("keydown", (e) => {
      const rs = rows(p), i = rs.findIndex((r) => r.classList.contains("is-active"));
      const go = (j) => { e.preventDefault(); setActive(p, rs[Math.max(0, Math.min(rs.length - 1, j))], true); };
      if (e.key === "ArrowDown") go(i < 0 ? 0 : i + 1);
      else if (e.key === "ArrowUp") go(i < 0 ? rs.length - 1 : i - 1);
      else if (e.key === "Home" && !input.value) go(0);
      else if (e.key === "End" && !input.value) go(rs.length - 1);
      else if (e.key === "PageDown") go(i + 10);
      else if (e.key === "PageUp") go(i - 10);
      else if (e.key === "Enter" && i >= 0) { e.preventDefault(); pick(p, rs[i]); }
      else if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); if (input.value) { input.value = ""; filter(p, ""); } else close(p); }
    });
    p.addEventListener("pointermove", (e) => { const r = e.target.closest(".pk-row"); if (r && rows(p).includes(r) && !r.classList.contains("is-active")) setActive(p, r); });
    p.addEventListener("click", (e) => { const r = e.target.closest(".pk-row"); if (r && rows(p).includes(r)) pick(p, r); });
    sub?.addEventListener("click", (e) => { const r = e.target.closest(".pk-row"); if (r) pick(p, r); });
    // Static fixture states: center the current model in anchored pickers, line the effort submenu up with its row.
    if (t) {
      t.addEventListener("click", () => (p.hidden ? open(p, t) : close(p)));
      // A press outside the picker, its effort list and its trigger closes it.
      document.addEventListener("pointerdown", (e) => { if (!p.hidden && p.isConnected && !p.contains(e.target) && !sub?.contains(e.target) && !t.contains(e.target)) close(p); });
      requestAnimationFrame(() => { if (p.hidden) return; place(p, t); const cur = p.querySelector('.pk-list [aria-selected="true"]'), l = p.querySelector(".pk-list"); if (cur) l.scrollTop = cur.offsetTop - l.clientHeight / 2 + cur.offsetHeight / 2; });
    }
    const pair = p.parentElement?.classList.contains("pk-pair") && p.nextElementSibling;
    if (pair) requestAnimationFrame(() => { const r = p.querySelector(".pk-more"); pair.style.marginTop = r.getBoundingClientRect().top - p.getBoundingClientRect().top - 6 + "px"; });
  };
  const run = () => document.querySelectorAll(".model-picker:not(.pk-sub)").forEach((p) => init(p));
  window.MPPicker = { init };
  document.readyState === "loading" ? document.addEventListener("DOMContentLoaded", run) : run();
})();
