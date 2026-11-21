// Mainplane line icons: drawn by hand on a 24 pt grid, round caps and joins.
// Optical size from a study of SF Symbols Regular (macOS 26, measured 2026-11-18): the stroke is about 0.09 x the
// text size (1.25 pt at 13 pt) and glyphs fill the box (plus 0.8 em, folder 1.1 em wide). So paths are drawn on the
// old 24 grid and scaled 1.15 about the center, with a 1.55 stroke (1.78 effective): 1.2 px at the 16 px default.
// <mp-icon name="send"></mp-icon>, or MPIcon.svg("send") for a string. Size: CSS width/height (default 1.25em).
// Shapes: "M..." path, "c x y r" circle, "C x y r" filled circle, "r x y w h rx" rect.
(() => {
  const I = {
    plus: ["M12 5v14M5 12h14"],
    minus: ["M5 12h14"],
    close: ["M6.5 6.5l11 11M17.5 6.5l-11 11"],
    check: ["M5 12.5l4.5 4.5L19 7"],
    "chevron-left": ["M14.5 5.5 8 12l6.5 6.5"],
    "chevron-right": ["M9.5 5.5 16 12l-6.5 6.5"],
    "chevron-up": ["M5.5 14.5 12 8l6.5 6.5"],
    "chevron-down": ["M5.5 9.5 12 16l6.5-6.5"],
    "chevron-up-down": ["M8 9.5l4-4 4 4M8 14.5l4 4 4-4"],
    "arrow-up": ["M12 19V5M6 11l6-6 6 6"],
    "arrow-right": ["M5 12h14M13 6l6 6-6 6"],
    "arrow-left": ["M19 12H5M11 6l-6 6 6 6"],
    search: ["c 10.5 10.5 6", "M15 15l5 5"],
    paperclip: ["M16.5 9v7a4.5 4.5 0 0 1-9 0V6a3 3 0 0 1 6 0v9.5a1.5 1.5 0 0 1-3 0V9", 40],
    image: ["r 3.5 5 17 14 2.5", "c 9 10 1.5", "M3.5 16.5l5-5 4 4 3-3 5 5"],
    file: ["M13.5 3.5h-6a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2h9a2 2 0 0 0 2-2v-10z", "M13.5 3.5v5h5"],
    folder: ["M3.5 7.5a2 2 0 0 1 2-2h4l2 2h7a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2z"],
    link: ["M10.5 13.5a3.5 3.5 0 0 0 5 0l3-3a3.5 3.5 0 0 0-5-5l-1 1", "M13.5 10.5a3.5 3.5 0 0 0-5 0l-3 3a3.5 3.5 0 0 0 5 5l1-1"],
    external: ["M14 4.5h5.5V10M19.5 4.5 11 13", "M17.5 14v4a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V8.5a2 2 0 0 1 2-2h4"],
    copy: ["r 8.5 8.5 11 11 2", "M15.5 8.5v-2a2 2 0 0 0-2-2h-7a2 2 0 0 0-2 2v7a2 2 0 0 0 2 2h2"],
    trash: ["M4.5 6.5h15M9.5 6.5V5A1.5 1.5 0 0 1 11 3.5h2A1.5 1.5 0 0 1 14.5 5v1.5", "M6.5 6.5l.8 12a2 2 0 0 0 2 1.9h5.4a2 2 0 0 0 2-1.9l.8-12M10 10.5v6M14 10.5v6"],
    pencil: ["M15.5 5 19 8.5 9 18.5l-4.5 1 1-4.5z", "M13.5 7l3.5 3.5"],
    compose: ["M11 4.5H6.5a2 2 0 0 0-2 2v11a2 2 0 0 0 2 2h11a2 2 0 0 0 2-2V13", "M17.5 3.5l3 3-8 8-3.8.8.8-3.8z"],
    more: ["C 6 12 1.4", "C 12 12 1.4", "C 18 12 1.4"],
    "more-vertical": ["C 12 6 1.4", "C 12 12 1.4", "C 12 18 1.4"],
    settings: ["M10.67 5.43L10.87 3.37L13.13 3.37L13.33 5.43L15.70 6.42L17.30 5.10L18.90 6.70L17.58 8.30L18.57 10.67L20.63 10.87L20.63 13.13L18.57 13.33L17.58 15.70L18.90 17.30L17.30 18.90L15.70 17.58L13.33 18.57L13.13 20.63L10.87 20.63L10.67 18.57L8.30 17.58L6.70 18.90L5.10 17.30L6.42 15.70L5.43 13.33L3.37 13.13L3.37 10.87L5.43 10.67L6.42 8.30L5.10 6.70L6.70 5.10L8.30 6.42z", "c 12 12 2.6"],
    pin: ["M9 4h6M10 4.5v5L7 13v1.5h10V13l-3-3.5v-5", "M12 14.5V20"],
    camera: ["M4 8.5a2 2 0 0 1 2-2h2l1.5-2h5l1.5 2h2a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2z", "c 12 13 3.5"],
    "clear": ["F 12 12 8.5", "K M9.2 9.2l5.6 5.6M14.8 9.2l-5.6 5.6"],
    sidebar: ["r 3.5 5 17 14 2.5", "M9.5 5v14"],
    stop: ["r 6.5 6.5 11 11 2"],
    play: ["M8 5.5v13l10.5-6.5z"],
    refresh: ["M19 12a7 7 0 1 1-2.05-4.95", "M19 4.5V8h-3.5"],
    clock: ["c 12 12 8.5", "M12 7.5V12l3 2"],
    calendar: ["r 4 5.5 16 14.5 2.5", "M4 10h16M8.5 3.5v4M15.5 3.5v4"],
    bell: ["M5 17.5h14l-1.5-2V11a5.5 5.5 0 0 0-11 0v4.5z", "M10 20.5h4"],
    person: ["c 12 8.5 3.5", "M5 20a7 7 0 0 1 14 0"],
    people: ["c 9 9 3", "M3.5 19a5.5 5.5 0 0 1 11 0", "c 16.5 8.5 2.5", "M16 13.5a4.8 4.8 0 0 1 4.5 4.8"],
    agent: ["r 5 8 14 11 3.5", "M12 8V5.5", "C 12 4.5 1", "C 9.5 13.5 1.1", "C 14.5 13.5 1.1", "M3 12.5V15M21 12.5V15"],
    terminal: ["r 3.5 5 17 14 2.5", "M7.5 10l2.5 2-2.5 2M12 14.5h4"],
    code: ["M8.5 7.5 4 12l4.5 4.5M15.5 7.5 20 12l-4.5 4.5M13.5 5l-3 14"],
    computer: ["r 3.5 4.5 17 11.5 2", "M9 20h6M12 16v4"],
    server: ["r 4 4.5 16 6.5 2", "r 4 13 16 6.5 2", "C 7.5 7.75 .9", "C 7.5 16.25 .9"],
    globe: ["c 12 12 8.5", "M3.5 12h17M12 3.5c-2.4 2.5-3.5 5.5-3.5 8.5s1.1 6 3.5 8.5M12 3.5c2.4 2.5 3.5 5.5 3.5 8.5s-1.1 6-3.5 8.5"],
    lock: ["r 5.5 10.5 13 9.5 2", "M8.5 10.5V8a3.5 3.5 0 0 1 7 0v2.5"],
    key: ["c 8 15.5 4", "M11 12.5l8-8M16.5 7 19 9.5M14 9.5l2 2"],
    eye: ["M2.5 12S6 6 12 6s9.5 6 9.5 6-3.5 6-9.5 6-9.5-6-9.5-6z", "c 12 12 2.75"],
    "eye-off": ["M2.5 12S6 6 12 6s9.5 6 9.5 6-3.5 6-9.5 6-9.5-6-9.5-6z", "c 12 12 2.75", "M4.5 4.5l15 15"],
    info: ["c 12 12 8.5", "M12 11v5.5", "C 12 7.8 .95"],
    question: ["c 12 12 8.5", "M9.6 9.6a2.5 2.5 0 1 1 3.5 2.2c-.7.3-1.1.9-1.1 1.6v.4", "C 12 16.8 .95"],
    warning: ["M10.3 4.9a2 2 0 0 1 3.4 0l7 12.1a2 2 0 0 1-1.7 3H5a2 2 0 0 1-1.7-3z", "M12 9.5v4", "C 12 16.6 .95"],
    error: ["c 12 12 8.5", "M9 9l6 6M15 9l-6 6"],
    "check-circle": ["c 12 12 8.5", "M8 12.3l2.7 2.7L16 9.5"],
    star: ["M12 3.8l2.5 5.2 5.7.7-4.2 3.9 1.1 5.7-5.1-2.8-5.1 2.8 1.1-5.7-4.2-3.9 5.7-.7z"],
    heart: ["M12 19.5s-7.5-4.5-7.5-10A4 4 0 0 1 12 7.5a4 4 0 0 1 7.5 2c0 5.5-7.5 10-7.5 10z"],
    "thumbs-up": ["M7.5 10.5v9", "M7.5 10.5 11 4a2 2 0 0 1 2.5 1.6L13 9.5h5a2 2 0 0 1 2 2.3l-1.1 6.4a2 2 0 0 1-2 1.8H7.5", "M7.5 10.5H5a1 1 0 0 0-1 1v7a1 1 0 0 0 1 1h2.5"],
    smile: ["c 12 12 8.5", "M8.8 14.2a4 4 0 0 0 6.4 0", "C 9.3 10 1", "C 14.7 10 1"],
    "smile-plus": ["M20.4 11.2a8.5 8.5 0 1 1-7.6-7.6", "M8.8 14.2a4 4 0 0 0 6.4 0", "C 9.3 10 1", "C 14.7 10 1", "M18.5 2.5v5M16 5h5"],
    reply: ["M9.5 6.5l-5 5 5 5", "M4.5 11.5h9a6 6 0 0 1 6 6v1"],
    message: ["M6.5 16.5a2 2 0 0 1-2-2v-8a2 2 0 0 1 2-2h11a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H11L6.5 20z"],
    thread: ["M6.5 16.5a2 2 0 0 1-2-2v-8a2 2 0 0 1 2-2h11a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H11L6.5 20z", "M8 9h8M8 12h5"],
    hash: ["M10 4 8.5 20M15.5 4 14 20M4.5 9h15M4 15h15"],
    download: ["M12 4v11M7 10.5l5 5 5-5M5 19.5h14"],
    upload: ["M12 15.5v-11M7 9l5-5 5 5M5 19.5h14"],
    filter: ["M4.5 6h15M7.5 12h9M10.5 18h3"],
    sort: ["M8 4.5v15M4.5 16 8 19.5l3.5-3.5M16 19.5v-15M12.5 8 16 4.5 19.5 8"],
    grid: ["r 4 4 6.5 6.5 1.5", "r 13.5 4 6.5 6.5 1.5", "r 4 13.5 6.5 6.5 1.5", "r 13.5 13.5 6.5 6.5 1.5"],
    list: ["M9 6.5h11M9 12h11M9 17.5h11", "C 5 6.5 1.1", "C 5 12 1.1", "C 5 17.5 1.1"],
    chart: ["M5.5 19.5V13M10 19.5V8M14.5 19.5V11M19 19.5V5"],
    "chart-line": ["M4 19.5h16", "M4.5 15.5l4.5-5 4 3 6.5-8"],
    home: ["M4.5 10.5 12 4.5l7.5 6V18a1.5 1.5 0 0 1-1.5 1.5H14v-5h-4v5H6A1.5 1.5 0 0 1 4.5 18z"],
    mic: ["r 9 3.5 6 10.5 3", "M6 11a6 6 0 0 0 12 0M12 17v3.5"],
    waveform: ["M4 10.5v3M8 7.5v9M12 4.5v15M16 8v8M20 10.5v3"],
    sliders: ["M4 7h9M17 7h3M4 17h3M11 17h9", "c 15 7 2", "c 9 17 2"],
    wrench: ["M14.5 4.5a4.5 4.5 0 0 0-4.2 6.1l-5.8 5.8a2 2 0 0 0 2.8 2.8l5.8-5.8a4.5 4.5 0 0 0 6.1-4.2l-2.7 2.7-2.4-.4-.4-2.4z"],
    sun: ["c 12 12 3.5", "M12 3.5v2M12 18.5v2M3.5 12h2M18.5 12h2M6 6l1.4 1.4M16.6 16.6 18 18M6 18l1.4-1.4M16.6 7.4 18 6"],
    moon: ["M19 14.5A7.5 7.5 0 0 1 9.5 5a7.5 7.5 0 1 0 9.5 9.5z"],
    expand: ["M14 4.5h5.5V10M10 19.5H4.5V14M19.5 4.5 14 10M4.5 19.5 10 14"],
    "zoom-in": ["c 10.5 10.5 6", "M15 15l5 5M8 10.5h5M10.5 8v5"],
  };
  const el = (s) => {
    if (s[0] === "M") return `<path d="${s}"/>`;
    const [k, ...n] = s.split(" ");
    if (k === "c") return `<circle cx="${n[0]}" cy="${n[1]}" r="${n[2]}"/>`;
    if (k === "K") return `<path d="${n.join(" ")}" stroke="var(--icon-knock, #fff)"/>`;
    if (k === "F") return `<circle cx="${n[0]}" cy="${n[1]}" r="${n[2]}" fill="currentColor" stroke="none" opacity=".45"/>`;
    if (k === "C") return `<circle cx="${n[0]}" cy="${n[1]}" r="${n[2]}" fill="currentColor" stroke="none"/>`;
    if (k === "r") return `<rect x="${n[0]}" y="${n[1]}" width="${n[2]}" height="${n[3]}" rx="${n[4]}"/>`;
    throw new Error("bad icon shape " + s);
  };
  const svg = (name) => {
    const d = I[name];
    if (!d) throw new Error("no icon " + name);
    const rot = typeof d[d.length - 1] === "number" ? d[d.length - 1] : 0;
    const body = d.filter((s) => typeof s === "string").map(el).join("");
    return `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><g transform="translate(12 12) scale(1.15) translate(-12 -12)${rot ? ` rotate(${rot} 12 12)` : ""}">${body}</g></svg>`;
  };
  window.MPIcon = { svg, names: Object.keys(I) };
  if (!customElements.get("mp-icon"))
    customElements.define("mp-icon", class extends HTMLElement {
      static observedAttributes = ["name"];
      get name() { return this.getAttribute("name"); }
      set name(v) { this.setAttribute("name", v); }
      connectedCallback() { this.render(); }
      attributeChangedCallback() { if (this.isConnected) this.render(); }
      render() { this.innerHTML = svg(this.getAttribute("name")); }
    });
})();
