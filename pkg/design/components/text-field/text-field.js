// Text area fixture: show the "Grows, then scrolls" demo scrolled to its end, as it is while someone types.
document.querySelectorAll("textarea[data-end]").forEach((t) => (t.scrollTop = t.scrollHeight));
