// Tooltip behavior for every [data-tip] control (label), with an optional [data-tip-key] shortcut ("⌘F"). One tag, in body.
// 500 ms hover delay. After a tag hides, the next one within 600 ms shows at once with no fade (moving along a toolbar).
// Hides on leave, press (and stays hidden until the pointer leaves), Esc, scroll. Keyboard focus shows it at once. No touch.
(() => {
  const DELAY = 500, WARM = 600, GAP = 6, EDGE = 8;
  let tip, cur, timer, warmUntil = 0, pressed;
  function show(el, instant) {
    if (!tip) { tip = document.createElement("div"); tip.className = "tooltip live"; tip.id = "mp-tooltip"; tip.setAttribute("role", "tooltip"); document.body.append(tip); }
    tip.textContent = el.dataset.tip;
    if (el.dataset.tipKey) { const k = document.createElement("kbd"); k.textContent = el.dataset.tipKey; tip.append(k); }
    const th = el.closest("[data-theme]")?.dataset.theme;
    if (th) tip.dataset.theme = th; else delete tip.dataset.theme;
    tip.classList.remove("show"); tip.classList.toggle("instant", instant); tip.style.left = tip.style.top = "0px";
    const r = el.getBoundingClientRect(), w = tip.offsetWidth, h = tip.offsetHeight;
    const above = r.bottom + GAP + h > innerHeight - EDGE && r.top - GAP - h >= EDGE;
    tip.style.left = Math.round(Math.min(Math.max(r.left + r.width / 2 - w / 2, EDGE), innerWidth - EDGE - w)) + "px";
    tip.style.top = Math.round(above ? r.top - GAP - h : r.bottom + GAP) + "px";
    if (el.dataset.tip !== el.getAttribute("aria-label")) el.setAttribute("aria-describedby", tip.id);
    tip.offsetWidth; tip.classList.add("show");
  }
  function hide() {
    clearTimeout(timer);
    if (tip?.classList.contains("show")) { warmUntil = Date.now() + WARM; tip.classList.remove("instant", "show"); }
    if (cur?.getAttribute("aria-describedby") === "mp-tooltip") cur.removeAttribute("aria-describedby");
    cur = null;
  }
  function enter(el, instant) {
    if (el === cur) return;
    hide(); if (!el) return;
    cur = el; const warm = instant || Date.now() < warmUntil;
    timer = setTimeout(() => show(el, warm), warm ? 0 : DELAY);
  }
  document.addEventListener("pointerover", (e) => {
    if (e.pointerType === "touch") return;
    const el = e.target.closest?.("[data-tip]");
    if (el !== pressed) pressed = null;
    if (!pressed) enter(el);
  });
  document.addEventListener("pointerout", (e) => { if (!e.relatedTarget) { pressed = null; hide(); } });
  document.addEventListener("pointerdown", () => { pressed = cur; hide(); }, true);
  document.addEventListener("keydown", (e) => e.key === "Escape" && hide(), true);
  document.addEventListener("scroll", hide, true);
  document.addEventListener("focusin", (e) => { const el = e.target.closest?.("[data-tip]"); if (el?.matches(":focus-visible")) enter(el, true); });
  document.addEventListener("focusout", (e) => e.target === cur && hide());
})();
