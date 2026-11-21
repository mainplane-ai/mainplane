// Search field: the clear button and Esc empty the field (Esc on an empty field blurs it, as in macOS) and fire input.
(() => {
  const clear = (i) => { i.value = ""; i.dispatchEvent(new Event("input", { bubbles: true })); };
  document.addEventListener("mousedown", (e) => { if (e.target.closest?.(".search > .clear")) e.preventDefault(); });
  document.addEventListener("click", (e) => {
    const b = e.target.closest?.(".search > .clear"); if (!b) return;
    const i = b.parentElement.querySelector("input"); clear(i); i.focus();
  });
  document.addEventListener("keydown", (e) => {
    const i = e.target; if (e.key !== "Escape" || !i.matches?.(".search > input")) return;
    if (i.value) clear(i); else i.blur();
  });
})();
