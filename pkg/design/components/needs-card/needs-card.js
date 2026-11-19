// Needs you card: Reply toggles the inline field and focuses it; Return sends, Shift+Return is a new line,
// Esc closes and gives focus back to Reply (the draft stays). A send fires "mp-reply" on the .need, with
// detail.text = the quote line, a blank line, then the reply. A dashboard passes it to the bridge's send.
(() => {
  const cut = (t, n = 40) => { if (t.length <= n) return t; const s = t.slice(0, n + 1).lastIndexOf(" "); return (s >= 24 ? t.slice(0, s) : t.slice(0, n)).replace(/[\s,.;:?!]+$/, "") + "…"; };
  const need = (el) => el.closest(".need:has(.reply-field)");
  const toggle = (n, open) => {
    n.classList.toggle("replying", open);
    n.querySelector(".reply").setAttribute("aria-expanded", open);
    if (open) n.querySelector("textarea").focus({ preventScroll: true });
  };
  const send = (n) => {
    const ta = n.querySelector("textarea"), text = ta.value.trim();
    if (!text) return;
    n.dispatchEvent(new CustomEvent("mp-reply", { bubbles: true, detail: { text: `Replying to: "${cut(n.querySelector("h3").textContent)}"\n\n${text}` } }));
    const r = n.querySelector(".replied"); r.querySelector("span").textContent = text; r.hidden = false;
    ta.value = ""; ta.closest(".reply-field").classList.remove("has-text");
    toggle(n, false); n.querySelector(".reply").focus();
  };
  document.addEventListener("click", (e) => {
    const b = e.target.closest(".btn"), n = b && need(b);
    if (!n) return;
    if (b.classList.contains("reply")) toggle(n, !n.classList.contains("replying"));
    else if (b.classList.contains("send")) send(n);
  });
  document.addEventListener("input", (e) => {
    if (e.target.matches?.("textarea") && need(e.target)) e.target.closest(".reply-field").classList.toggle("has-text", !!e.target.value.trim());
  });
  document.addEventListener("keydown", (e) => {
    const n = e.target.matches?.("textarea") && need(e.target);
    if (!n || e.isComposing) return;
    if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); send(n); }
    else if (e.key === "Escape") { toggle(n, false); n.querySelector(".reply").focus(); }
  });
})();
