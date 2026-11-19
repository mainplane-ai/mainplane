// Needs you card behavior (demo of the dashboard code). Reply toggles the inline field and focuses it; Return sends,
// Shift+Return is a new line, Esc closes and gives focus back to Reply (the draft stays). The send is the bridge's
// send(text) with the quote line first (NOTES.md). Here it is printed into the panel's .nc-wire instead.
(() => {
  const cut = (t, n = 40) => { if (t.length <= n) return t; const s = t.slice(0, n + 1).lastIndexOf(" "); return (s >= 24 ? t.slice(0, s) : t.slice(0, n)).replace(/[\s,.;:?!]+$/, "") + "…"; };
  const live = (el) => el.closest(".nc-live .need");
  const toggle = (need, open) => {
    need.classList.toggle("replying", open);
    need.querySelector(".reply").setAttribute("aria-expanded", open);
    if (open) need.querySelector("textarea").focus({ preventScroll: true });
  };
  const send = (need) => {
    const ta = need.querySelector("textarea"), text = ta.value.trim();
    if (!text) return;
    const msg = `Replying to: "${cut(need.querySelector("h3").textContent)}"\n\n${text}`;
    need.closest(".pn").querySelector(".nc-wire").textContent = "send(text):\n" + msg; // real page: bridge send(msg)
    const r = need.querySelector(".replied"); r.querySelector("span").textContent = text; r.hidden = false;
    ta.value = ""; ta.closest(".reply-field").classList.remove("has-text");
    toggle(need, false); need.querySelector(".reply").focus();
  };
  document.addEventListener("click", (e) => {
    const b = e.target.closest(".btn"), need = b && live(b);
    if (!need) return;
    if (b.classList.contains("reply")) toggle(need, !need.classList.contains("replying"));
    else if (b.classList.contains("send")) send(need);
  });
  document.addEventListener("input", (e) => {
    if (live(e.target)) e.target.closest(".reply-field").classList.toggle("has-text", !!e.target.value.trim());
  });
  document.addEventListener("keydown", (e) => {
    const need = e.target.matches?.("textarea") && live(e.target);
    if (!need || e.isComposing) return;
    if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); send(need); }
    else if (e.key === "Escape") { toggle(need, false); need.querySelector(".reply").focus(); }
  });
})();
