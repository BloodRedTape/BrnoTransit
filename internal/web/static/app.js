const MAX_SUGGESTIONS = 8;

const fold = (s) =>
  s.normalize("NFD").replace(/\p{M}/gu, "").toLowerCase().replace(/\s+/g, " ").trim();

function loadStops() {
  const el = document.getElementById("stops-data");
  if (!el) return [];
  return JSON.parse(el.textContent).map((name) => {
    const f = fold(name);
    return { name, folded: f, words: f.split(/[\s,.\-()]+/).filter(Boolean) };
  });
}

function rank(stop, q, qWords) {
  if (stop.folded === q) return 0;
  if (stop.folded.startsWith(q)) return 1;
  if (qWords.every((w) => stop.words.some((sw) => sw.startsWith(w)))) return 2;
  if (stop.folded.includes(q)) return 3;
  return -1;
}

function search(stops, query) {
  const q = fold(query);
  if (!q) return [];
  const qWords = q.split(/[\s,.\-()]+/).filter(Boolean);
  const hits = [];
  for (const s of stops) {
    const r = rank(s, q, qWords);
    if (r >= 0) hits.push([r, s]);
  }
  hits.sort((a, b) => a[0] - b[0] || a[1].name.length - b[1].name.length || a[1].name.localeCompare(b[1].name, "cs"));
  return hits.slice(0, MAX_SUGGESTIONS).map((h) => h[1].name);
}

function attachAutocomplete(input, stops) {
  const list = document.getElementById(input.getAttribute("aria-controls"));
  let items = [];
  let active = -1;

  const close = () => {
    list.hidden = true;
    input.setAttribute("aria-expanded", "false");
    active = -1;
  };

  const highlight = (i) => {
    active = i;
    [...list.children].forEach((li, k) => li.setAttribute("aria-selected", String(k === i)));
    if (i >= 0) list.children[i].scrollIntoView({ block: "nearest" });
  };

  const choose = (name) => {
    input.value = name;
    close();
  };

  const render = () => {
    items = search(stops, input.value);
    list.replaceChildren(
      ...items.map((name, i) => {
        const li = document.createElement("li");
        li.role = "option";
        li.id = `${list.id}-${i}`;
        li.textContent = name;
        li.addEventListener("mousedown", (e) => {
          e.preventDefault();
          choose(name);
        });
        return li;
      }),
    );
    const exact = items.length === 1 && fold(items[0]) === fold(input.value);
    if (items.length === 0 || exact) return close();
    list.hidden = false;
    input.setAttribute("aria-expanded", "true");
    highlight(-1);
  };

  input.addEventListener("input", render);
  input.addEventListener("focus", () => input.value && render());
  input.addEventListener("blur", close);
  input.addEventListener("keydown", (e) => {
    if (list.hidden) return;
    if (e.key === "ArrowDown") {
      e.preventDefault();
      highlight((active + 1) % items.length);
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      highlight(active <= 0 ? items.length - 1 : active - 1);
    } else if (e.key === "Enter" || e.key === "Tab") {
      const pick = active >= 0 ? active : 0;
      if (items[pick] && (e.key === "Enter" || fold(items[pick]) !== fold(input.value))) {
        if (e.key === "Enter") e.preventDefault();
        choose(items[pick]);
      }
    } else if (e.key === "Escape") {
      close();
    }
  });
}

document.addEventListener("DOMContentLoaded", () => {
  const stops = loadStops();
  document.querySelectorAll(".stop-input").forEach((input) => attachAutocomplete(input, stops));

  const swap = document.getElementById("swap");
  if (!swap) return;
  swap.addEventListener("click", () => {
    const from = document.getElementById("from");
    const to = document.getElementById("to");
    [from.value, to.value] = [to.value, from.value];
  });
});
