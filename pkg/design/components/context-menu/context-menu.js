// Menu behavior. MPMenu.open(menu, {x, y} | anchorEl): shows a .menu as .floating at a point (context menu) or under an anchor,
// kept inside the viewport. It opens at once (Apple and Slack: no open animation); focus goes to the menu, not to a row,
// so a mouse open shows no highlight. Arrow keys move, Home/End, Enter/Space choose, ArrowRight/Left open/close a submenu,
// Escape or an outside click closes. Hover moves focus, so mouse and keyboard share one highlight; leaving the menu clears it.
// Choosing a row blinks it once, then the menu fades out (macOS). A submenu is a .menu right after its row
// (<button role="menuitem" aria-haspopup="menu">); it opens on hover or ArrowRight with its first row level with the parent row.
// MPMenu.context(el, menu): opens menu on right click / long press of el and outlines el (.menu-target) while open.
(() => {
  const stack = []; let back = null, target = null;
  const rows = (m) => [...m.querySelectorAll(':scope > [role^="menuitem"]:not(:disabled):not([aria-disabled="true"])')];
  const top = () => stack[stack.length - 1];
  const sub = (row) => row?.getAttribute("aria-haspopup") === "menu" ? row.nextElementSibling : null;
  function place(m, x, y, flipY) {
    const w = m.offsetWidth, h = m.offsetHeight;
    m.style.left = Math.max(8, Math.min(x, innerWidth - w - 8)) + "px";
    m.style.top = (y + h > innerHeight - 8 ? Math.max(8, flipY ?? innerHeight - h - 8) : y) + "px";
  }
  function closeTo(n) {
    while (stack.length > n) {
      const m = stack.pop();
      m.hidden = true; m.classList.remove("floating", "closing");
      m.previousElementSibling?.setAttribute?.("aria-expanded", "false");
    }
  }
  function close(fade) {
    if (!stack.length) return;
    const all = [...stack];
    target?.classList.remove("menu-target"); target = null;
    document.removeEventListener("pointerdown", outside, true); document.removeEventListener("keydown", keys, true);
    back?.focus?.({ preventScroll: true }); back = null;
    if (!fade || matchMedia("(prefers-reduced-motion: reduce)").matches) return closeTo(0);
    all.forEach((m) => m.classList.add("closing"));
    setTimeout(() => { if (stack[0] === all[0]) closeTo(0); }, 160);
  }
  function openSub(row, focusFirst) {
    const m = sub(row), parent = row.parentElement, i = stack.indexOf(parent);
    if (!m || i < 0) return;
    closeTo(i + 1);
    m.hidden = false; m.classList.add("floating"); m.tabIndex = -1; stack.push(m); row.setAttribute("aria-expanded", "true");
    const r = row.getBoundingClientRect(), p = parent.getBoundingClientRect(), pad = parseFloat(getComputedStyle(m).paddingTop);
    const x = p.right - 3 + m.offsetWidth > innerWidth - 8 ? p.left + 3 - m.offsetWidth : p.right - 3;
    place(m, x, r.top - pad);
    if (focusFirst) rows(m)[0]?.focus({ preventScroll: true });
  }
  const outside = (e) => { if (!stack.some((m) => m.contains(e.target))) close(); };
  function keys(e) {
    const m = stack.find((s) => s.contains(document.activeElement) && document.activeElement.parentElement === s) || top();
    const it = rows(m), i = it.indexOf(document.activeElement), k = e.key;
    if (k === "Escape") { e.preventDefault(); stack.length > 1 && m !== stack[0] ? (closeTo(stack.indexOf(m)), m.previousElementSibling.focus()) : close(); }
    else if (k === "ArrowDown" || k === "ArrowUp") { e.preventDefault(); it[i < 0 ? (k === "ArrowDown" ? 0 : it.length - 1) : (i + (k === "ArrowDown" ? 1 : it.length - 1)) % it.length]?.focus(); }
    else if (k === "Home" || k === "End") { e.preventDefault(); it[k === "Home" ? 0 : it.length - 1]?.focus(); }
    else if (k === "ArrowRight" && sub(it[i])) { e.preventDefault(); openSub(it[i], true); }
    else if (k === "ArrowLeft" && m !== stack[0]) { e.preventDefault(); closeTo(stack.indexOf(m)); m.previousElementSibling.focus(); }
    else if ((k === "Enter" || k === " ") && it[i]) { e.preventDefault(); choose(it[i]); }
    else if (k === "Tab") { e.preventDefault(); close(); }
  }
  function choose(row) {
    if (sub(row)) return openSub(row, true);
    row.classList.add("is-blink");
    setTimeout(() => { row.classList.remove("is-blink"); row.click(); close(true); }, 70);
  }
  function openMenu(m, at, el) {
    close();
    back = document.activeElement; stack.push(m);
    m.hidden = false; m.classList.add("floating"); m.tabIndex = -1;
    if (at instanceof Element) { const r = at.getBoundingClientRect(); place(m, r.left, r.bottom + 6, r.top - 6 - m.offsetHeight); }
    else place(m, at.x, at.y, at.y - m.offsetHeight);
    target = el || null; target?.classList.add("menu-target");
    m.focus({ preventScroll: true });
    setTimeout(() => { document.addEventListener("pointerdown", outside, true); document.addEventListener("keydown", keys, true); });
  }
  // hover = focus, for every .menu on the page (floating or static), so only one row is ever highlighted
  // A pointer crossing other rows on its way into an open submenu gets 150 ms before the submenu closes (Apple's grace).
  let grace;
  document.addEventListener("pointerover", (e) => {
    const row = e.target.closest?.('.menu > [role^="menuitem"]');
    clearTimeout(grace);
    if (!row || row.disabled || row.getAttribute("aria-disabled") === "true") return;
    const i = stack.indexOf(row.parentElement), go = () => { row.focus({ preventScroll: true }); if (i >= 0) sub(row) ? openSub(row) : closeTo(i + 1); };
    stack.length > i + 1 && i >= 0 && stack[i + 1].previousElementSibling !== row ? (grace = setTimeout(go, 150)) : go();
  });
  document.addEventListener("pointerout", (e) => {
    const m = e.target.closest?.(".menu");
    const a = document.activeElement;
    if (!m || m.contains(e.relatedTarget) || a?.parentElement !== m || sub(a)) return;
    stack.includes(m) ? m.focus({ preventScroll: true }) : a.blur();
  });
  // a real click on a row of an open menu: blink, then the row's own click (untrusted, passes through), then close
  document.addEventListener("click", (e) => {
    const row = e.target.closest?.('.menu > [role^="menuitem"]');
    if (!e.isTrusted || !row || !stack.includes(row.parentElement)) return;
    e.preventDefault(); e.stopImmediatePropagation(); choose(row);
  }, true);
  function context(el, m) {
    el.addEventListener("contextmenu", (e) => { e.preventDefault(); openMenu(m, { x: e.clientX, y: e.clientY }, el); });
    let t;
    el.addEventListener("touchstart", (e) => { const p = e.touches[0]; t = setTimeout(() => openMenu(m, { x: p.clientX, y: p.clientY }, el), 500); }, { passive: true });
    el.addEventListener("touchend", () => clearTimeout(t));
    el.addEventListener("touchmove", () => clearTimeout(t));
  }
  window.MPMenu = { open: openMenu, close, context };
})();
