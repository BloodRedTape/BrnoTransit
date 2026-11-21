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

const MAP_STYLES = {
  dark: "https://tiles.versatiles.org/assets/styles/colorful-dark/style.json",
  light: "https://tiles.versatiles.org/assets/styles/colorful/style.json",
};
const MAP_THEME = {
  dark: { walk: "#d6d6d6", casing: "#0b0b0b", casingOpacity: 0.75, stopFill: "#1e1e1e" },
  light: { walk: "#5f5f66", casing: "#ffffff", casingOpacity: 0.95, stopFill: "#ffffff" },
};
const lightQuery = window.matchMedia("(prefers-color-scheme: light)");
const currentScheme = () => (lightQuery.matches ? "light" : "dark");

function luminance(hex) {
  const m = /^#?([0-9a-f]{6})$/i.exec(hex || "");
  if (!m) return 1;
  const n = parseInt(m[1], 16);
  return (0.299 * (n >> 16) + 0.587 * ((n >> 8) & 255) + 0.114 * (n & 255)) / 255;
}

function legColor(leg, scheme) {
  if (leg.w) return MAP_THEME[scheme].walk;
  const lum = luminance(leg.c);
  const unreadable = scheme === "dark" ? lum < 0.2 : lum > 0.85;
  return unreadable && leg.tc ? leg.tc : leg.c || "#4a8fdb";
}

const lonLat = ([lat, lon]) => [lon, lat];

function endpointMarker(kind, name) {
  const el = document.createElement("div");
  el.className = `mk mk-${kind}`;
  const pin = document.createElement("span");
  pin.className = "mk-pin";
  const label = document.createElement("span");
  label.className = "mk-label";
  label.textContent = name;
  el.append(pin, label);
  return el;
}

function routeGeoJSON(legs, scheme) {
  const lines = [];
  const stops = [];
  legs.forEach((leg, i) => {
    const color = legColor(leg, scheme);
    const coords = leg.p.map(lonLat);
    lines.push({ type: "Feature", properties: { color, walk: !!leg.w }, geometry: { type: "LineString", coordinates: coords } });
    if (leg.w) return;
    coords.forEach((c, k) => {
      const edge = k === 0 || k === coords.length - 1;
      const terminal = (i === 0 && k === 0) || (i === legs.length - 1 && k === coords.length - 1);
      if (terminal) return;
      const name = k === 0 ? leg.f : k === coords.length - 1 ? leg.t : "";
      stops.push({ type: "Feature", properties: { color, kind: edge ? "transfer" : "stop", name }, geometry: { type: "Point", coordinates: c } });
    });
  });
  return { lines: { type: "FeatureCollection", features: lines }, stops: { type: "FeatureCollection", features: stops } };
}

function addRouteLayers(map, legs, scheme) {
  const theme = MAP_THEME[scheme];
  const { lines, stops } = routeGeoJSON(legs, scheme);
  map.addSource("route", { type: "geojson", data: lines });
  map.addSource("route-stops", { type: "geojson", data: stops });
  map.addLayer({
    id: "route-casing", type: "line", source: "route", filter: ["!", ["get", "walk"]],
    layout: { "line-join": "round", "line-cap": "round" },
    paint: { "line-color": theme.casing, "line-width": 10, "line-opacity": theme.casingOpacity },
  });
  map.addLayer({
    id: "route-ride", type: "line", source: "route", filter: ["!", ["get", "walk"]],
    layout: { "line-join": "round", "line-cap": "round" },
    paint: { "line-color": ["get", "color"], "line-width": 6 },
  });
  map.addLayer({
    id: "route-walk", type: "line", source: "route", filter: ["get", "walk"],
    layout: { "line-join": "round", "line-cap": "round" },
    paint: { "line-color": ["get", "color"], "line-width": 4, "line-dasharray": [0, 2] },
  });
  map.addLayer({
    id: "route-stops", type: "circle", source: "route-stops", filter: ["==", ["get", "kind"], "stop"],
    paint: { "circle-radius": 3, "circle-color": theme.stopFill, "circle-stroke-color": ["get", "color"], "circle-stroke-width": 2 },
  });
  map.addLayer({
    id: "route-transfers", type: "circle", source: "route-stops", filter: ["==", ["get", "kind"], "transfer"],
    paint: { "circle-radius": 6, "circle-color": "#ffffff", "circle-stroke-color": ["get", "color"], "circle-stroke-width": 3 },
  });
}

function initRouteMap() {
  const el = document.getElementById("route-map");
  if (!el) return;
  if (!window.maplibregl) {
    el.textContent = "Map could not be loaded.";
    el.classList.add("map-error");
    return;
  }
  const legs = JSON.parse(el.dataset.map);
  const bounds = new maplibregl.LngLatBounds();
  legs.forEach((l) => l.p.forEach((p) => bounds.extend(lonLat(p))));

  let scheme = currentScheme();
  const map = new maplibregl.Map({
    container: el,
    style: MAP_STYLES[scheme],
    bounds,
    fitBoundsOptions: { padding: { top: 60, bottom: 50, left: 50, right: 50 }, maxZoom: 16 },
    attributionControl: { compact: true },
    dragRotate: false,
    pitchWithRotate: false,
  });
  map.touchZoomRotate.disableRotation();
  map.addControl(new maplibregl.NavigationControl({ showCompass: false }), "top-right");
  map.on("error", (e) => console.warn("map:", e.error && e.error.message));

  map.on("style.load", () => addRouteLayers(map, legs, scheme));
  lightQuery.addEventListener("change", () => {
    scheme = currentScheme();
    map.setStyle(MAP_STYLES[scheme]);
  });

  const popup = new maplibregl.Popup({ closeButton: false, closeOnClick: false, offset: 10, className: "stop-popup" });
  ["route-stops", "route-transfers"].forEach((layer) => {
    map.on("mouseenter", layer, (e) => {
      const f = e.features[0];
      if (!f.properties.name) return;
      map.getCanvas().style.cursor = "pointer";
      popup.setLngLat(f.geometry.coordinates).setText(f.properties.name).addTo(map);
    });
    map.on("mouseleave", layer, () => {
      map.getCanvas().style.cursor = "";
      popup.remove();
    });
  });

  const first = legs[0];
  const last = legs[legs.length - 1];
  new maplibregl.Marker({ element: endpointMarker("start", first.f), anchor: "left", offset: [-11, 0] })
    .setLngLat(lonLat(first.p[0])).addTo(map);
  new maplibregl.Marker({ element: endpointMarker("end", last.t), anchor: "bottom-left", offset: [-14, 0] })
    .setLngLat(lonLat(last.p[last.p.length - 1])).addTo(map);
}

function initHistoryBack() {
  document.querySelectorAll("[data-history-back]").forEach((a) => {
    a.addEventListener("click", (e) => {
      let ref;
      try { ref = new URL(document.referrer); } catch { return; }
      if (ref.origin === location.origin && ref.pathname === "/search" && history.length > 1) {
        e.preventDefault();
        history.back();
      }
    });
  });
}

document.addEventListener("DOMContentLoaded", () => {
  initRouteMap();
  initHistoryBack();
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

if ("serviceWorker" in navigator) {
  window.addEventListener("load", () => {
    navigator.serviceWorker.register("/sw.js").catch((e) => console.warn("service worker:", e));
  });
}
