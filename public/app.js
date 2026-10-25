// Draw a box, read CTrees' aboveground biomass for it through the Go Worker
// (/api, see main.go), and draw what came back: where it changed between two
// years (red for loss, blue for gain), or the biomass of one year. The
// Worker picks the resolution; this page colours the raw int16 cells, shows
// their summary, and the 26-year series from a coarse overview.

const $ = (id) => document.getElementById(id);

// The World Biomass Map's ramp (zarro/geographyart/assets/biomass-core.js), t/ha.
const RAMP = [[0, "#eef3e2"], [5, "#eef3e2"], [25, "#c6dba3"], [75, "#8fbf6a"], [150, "#4f9a45"], [250, "#2b6f3a"], [400, "#12432a"]];
// Change, as on the World Biomass Map: changes within ±band (the Worker's
// summary says how much, 10 t/ha by default) are left clear, the colour runs
// from pale at the band to full at ±150 t/ha.
const LOSS = "#c8442b", LOSS_EDGE = "#e9b4aa", GROWTH = "#2b7bb9", GROWTH_EDGE = "#aacae3";
const CHANGE_END = 150;
const MERCATOR_LIMIT = 85.0511;

const state = { ref: "ard-overviews", mode: "change", from: null, meta: null, bbox: null, year: null, region: null, regionError: null, series: null };
let regionAbort, seriesAbort;

// ── URL state ───────────────────────────────────────────────────────────
function readHash() {
  const p = new URLSearchParams(location.hash.slice(1));
  if (p.get("ref")) state.ref = p.get("ref");
  if (p.get("year")) state.year = Number(p.get("year")) || null;
  if (p.get("from")) state.from = Number(p.get("from")) || null;
  state.mode = p.get("mode") === "biomass" ? "biomass" : "change";
  const b = (p.get("bbox") || "").split(",").map(Number);
  if (b.length === 4 && b.every(Number.isFinite) && b[0] < b[2] && b[1] < b[3]) state.bbox = b;
}
function writeHash() {
  const p = new URLSearchParams({ ref: state.ref, mode: state.mode });
  if (state.mode === "change" && state.from) p.set("from", state.from);
  if (state.year) p.set("year", state.year);
  if (state.bbox) p.set("bbox", state.bbox.join(","));
  history.replaceState(null, "", "#" + p);
}

// ── Map ─────────────────────────────────────────────────────────────────
readHash();
const map = new maplibregl.Map({
  container: "map",
  style: "https://tiles.openfreemap.org/styles/positron",
  center: [10, 20], zoom: 1.6,
  boxZoom: false, // shift-drag draws a box instead
  attributionControl: { compact: true },
});
map.addControl(new maplibregl.NavigationControl({ showCompass: false }), "top-right");

map.on("load", () => {
  const firstSymbol = map.getStyle().layers.find((l) => l.type === "symbol")?.id;
  map.addSource("region", { type: "image", url: blankImage(), coordinates: [[0, 0], [0.001, 0], [0.001, -0.001], [0, -0.001]] });
  map.addLayer({ id: "region", type: "raster", source: "region", paint: { "raster-resampling": "nearest", "raster-fade-duration": 0, "raster-opacity": 0.92 } }, firstSymbol);
  map.addSource("box", { type: "geojson", data: emptyFC() });
  map.addLayer({ id: "box", type: "line", source: "box", paint: { "line-color": "#1f5a32", "line-width": 1.5 } });
  if (state.bbox) {
    showBox(state.bbox);
    map.fitBounds([[state.bbox[0], state.bbox[1]], [state.bbox[2], state.bbox[3]]], { padding: 80, duration: 0, maxZoom: 13 });
  }
  loadMeta().then(() => state.bbox && load());
});

function emptyFC() { return { type: "FeatureCollection", features: [] }; }
function blankImage() {
  const c = document.createElement("canvas");
  c.width = c.height = 1;
  return c.toDataURL();
}
function showBox([w, s, e, n], dashed = false) {
  map.getSource("box")?.setData({ type: "Feature", geometry: { type: "Polygon", coordinates: [[[w, n], [e, n], [e, s], [w, s], [w, n]]] } });
  map.setPaintProperty("box", "line-dasharray", dashed ? [2, 2] : [1, 0]);
}

// ── Drawing a box: the button, or shift-drag ────────────────────────────
let drawing = false, start = null, last = null;
function setDrawing(on) {
  drawing = on;
  $("draw").setAttribute("aria-pressed", String(on));
  $("map").classList.toggle("drawing", on);
}
$("draw").addEventListener("click", () => setDrawing(!drawing));
addEventListener("keydown", (e) => e.key === "Escape" && setDrawing(false));

function boxOf(a, b) {
  const r = (v) => Math.round(v * 1e5) / 1e5;
  return [r(Math.max(-180, Math.min(a.lng, b.lng))), r(Math.max(-90, Math.min(a.lat, b.lat))),
          r(Math.min(180, Math.max(a.lng, b.lng))), r(Math.min(90, Math.max(a.lat, b.lat)))];
}
function begin(e) {
  e.preventDefault();
  start = last = e.lngLat;
  map.dragPan.disable();
}
function move(e) {
  if (!start) return;
  last = e.lngLat;
  showBox(boxOf(start, last), true);
}
function finish(e) {
  if (!start) return;
  const a = map.project(start), b = map.project(last);
  const box = boxOf(start, last);
  start = null;
  map.dragPan.enable();
  setDrawing(false);
  if (Math.abs(a.x - b.x) < 4 || Math.abs(a.y - b.y) < 4) {
    if (state.bbox) showBox(state.bbox);
    return;
  }
  state.bbox = box;
  state.series = null;
  writeHash();
  load();
}
map.on("mousedown", (e) => (drawing || e.originalEvent.shiftKey) && begin(e));
map.on("touchstart", (e) => drawing && e.points.length === 1 && begin(e));
map.on("mousemove", move);
map.on("touchmove", move);
map.on("mouseup", finish);
map.on("touchend", finish);

// ── Reading ─────────────────────────────────────────────────────────────
async function getJSON(url, signal) {
  const res = await fetch(url, { signal });
  const body = await res.json();
  if (!res.ok) throw Object.assign(new Error(body.error || res.statusText), { status: res.status });
  return body;
}

// The page asks for the branch once, then reads the snapshot it points to,
// so every region and series response is immutable and cacheable.
async function loadMeta() {
  try {
    state.meta = await getJSON(`/api/meta?ref=${encodeURIComponent(state.ref)}`);
  } catch (err) {
    showResult();
    notes([err.message], true);
    return;
  }
  const { dataset, branches, tags } = state.meta;
  const sel = $("ref");
  const order = ["ard-overviews", "ard", "main"];
  const names = Object.keys(branches).sort((a, b) => ((order.indexOf(a) + 1) || 99) - ((order.indexOf(b) + 1) || 99) || a.localeCompare(b));
  sel.replaceChildren(...names.map((n) => new Option(n, n)));
  if (Object.keys(tags).length) {
    const g = document.createElement("optgroup");
    g.label = "tags";
    g.append(...Object.keys(tags).sort().reverse().map((n) => new Option(n, n)));
    sel.append(g);
  }
  sel.value = state.ref;
  const years = dataset.years;
  $("year").min = years[0];
  $("year").max = years[years.length - 1];
  if (!years.includes(state.year)) state.year = years[years.length - 1];
  if (!years.includes(state.from)) state.from = years[0];
  $("year").value = $("year-out").value = state.year;
  $("from").replaceChildren(...years.map((y) => new Option(y, y)));
  $("from").value = state.from;
  showMode();
}

// ── Change or biomass ───────────────────────────────────────────────────
function showMode() {
  const change = state.mode === "change";
  $("mode-change").setAttribute("aria-pressed", String(change));
  $("mode-biomass").setAttribute("aria-pressed", String(!change));
  $("from").hidden = $("legend").hidden = !change;
}
for (const mode of ["change", "biomass"]) {
  $(`mode-${mode}`).addEventListener("click", () => {
    if (state.mode === mode) return;
    state.mode = mode;
    showMode();
    writeHash();
    drawSeries();
    if (state.bbox) loadRegion();
  });
}
$("from").addEventListener("change", (e) => {
  state.from = Number(e.target.value);
  writeHash();
  drawSeries();
  if (state.bbox) loadRegion();
});

// Back, forward and edited URLs.
addEventListener("hashchange", async () => {
  const ref = state.ref;
  readHash();
  if (state.bbox) showBox(state.bbox);
  state.series = null;
  if (ref !== state.ref) await loadMeta();
  else {
    $("year").value = $("year-out").value = state.year;
    $("from").value = state.from;
    showMode();
  }
  if (state.bbox) load();
});

$("ref").addEventListener("change", async (e) => {
  state.ref = e.target.value;
  state.series = null;
  writeHash();
  await loadMeta();
  if (state.bbox) load();
});

let yearTimer;
$("year").addEventListener("input", (e) => {
  state.year = Number(e.target.value);
  $("year-out").value = state.year;
  drawSeries();
  clearTimeout(yearTimer);
  yearTimer = setTimeout(() => { writeHash(); loadRegion(); }, 120);
});

function load() {
  if (!state.meta) return;
  showResult();
  loadRegion();
  loadSeries();
}

function query(extra = {}) {
  return new URLSearchParams({ ref: state.meta.dataset.snapshot, bbox: state.bbox.join(","), ...extra });
}

async function loadRegion() {
  regionAbort?.abort();
  regionAbort = new AbortController();
  const t0 = performance.now();
  $("meta").textContent = "reading…";
  $("meta").classList.add("loading");
  try {
    const res = await fetch(`/api/region?${regionQuery()}`, { signal: regionAbort.signal });
    if (!res.ok) throw Object.assign(new Error((await res.json()).error), { status: res.status });
    const meta = JSON.parse(res.headers.get("x-region"));
    const data = new Int16Array(await res.arrayBuffer()); // little-endian, as every browser is
    state.region = { meta, data };
    state.regionError = null;
    render(meta, data);
    showBox(meta.bbox);
    const ms = Math.round(performance.now() - t0);
    $("meta").textContent = `${meta.level} · ${meta.cols}×${meta.rows} · ${ms} ms`;
    $("meta").title = `snapshot ${meta.snapshot}; ${meta.cell_degrees.toPrecision(4)}° cells; the Worker read for ${Math.round(meta.ms)} ms`;
    summary();
  } catch (err) {
    if (err.name === "AbortError") return;
    state.region = null;
    state.regionError = err.message;
    map.getSource("region").updateImage({ url: blankImage(), coordinates: [[0, 0], [0.001, 0], [0.001, -0.001], [0, -0.001]] });
    $("meta").textContent = "";
    $("dl").replaceChildren();
    summary();
    return;
  } finally {
    $("meta").classList.remove("loading");
  }
  const tif = (v, from) => `/api/region?${query({ year: state.year, ...(from ? { from } : {}), var: v, format: "tif" })}`;
  $("dl").innerHTML = state.mode === "change"
    ? `<a href="${tif("agb", state.from)}" download title="Change in biomass, ${state.from}–${state.year}">change.tif</a><a href="${tif("agb")}" download>agb.tif</a>`
    : `<a href="${tif("agb")}" download>agb.tif</a><a href="${tif("uncertainty")}" download title="Uncertainty (residual standard error)">±.tif</a>`;
}

function regionQuery() {
  return query(state.mode === "change" ? { year: state.year, from: state.from } : { year: state.year });
}

async function loadSeries() {
  seriesAbort?.abort();
  seriesAbort = new AbortController();
  try {
    state.series = (await getJSON(`/api/series?${query()}`, seriesAbort.signal)).series;
  } catch (err) {
    if (err.name === "AbortError") return;
    state.series = { error: err.status === 422 ? "No overviews on this ref, so no 26-year series: that needs ard-overviews." : err.message };
  }
  drawSeries();
  summary();
}

// ── Showing ─────────────────────────────────────────────────────────────
function showResult() {
  $("hint").hidden = true;
  $("result").hidden = false;
}

function summary() {
  const r = state.region;
  const out = [];
  $("mean").textContent = "–";
  $("total").textContent = "";
  if (state.regionError) out.push(state.regionError);
  $("mean").className = "";
  if (r && r.meta.from) {
    const s = r.meta.summary;
    $("mean").textContent = s.mean == null ? "no data" : signed(s.mean);
    $("mean").className = s.mean <= -1 ? "loss" : s.mean >= 1 ? "gain" : "";
    const pct = (ha) => `${percent(ha / s.area_ha)}`;
    $("total").textContent = !s.valid ? "" :
      `${pct(s.loss_ha)} lost` + (s.gain_ha / s.area_ha >= 0.01 ? `, ${pct(s.gain_ha)} gained` : "") +
      ` · ${s.total < 0 ? "−" : "+"}${mass(Math.abs(s.total))}`;
    $("total").title = `Share of ${area(s.area_ha)} whose biomass fell (rose) by at least ${s.band} t/ha from ${r.meta.from} to ${r.meta.year}, and the net change in tonnes.`;
  } else if (r) {
    const s = r.meta.summary;
    $("mean").textContent = s.mean == null ? "no data" : s.mean.toFixed(s.mean < 100 ? 1 : 0);
    $("total").textContent = s.valid ? `${mass(s.total)} over ${area(s.area_ha)}` : "";
    $("total").title = "Aboveground biomass (dry matter) summed over the valid cells; about half of it is carbon.";
    const attrs = state.meta.dataset.attributes.agb;
    if (r.meta.decoding.scale_factor === 1 && Number(attrs.agb_scale_factor) === 10) {
      out.push("On main, scale_factor is 1, so CF readers such as xarray (and this page) show biomass 10× too high. The ard branches set it to 0.1.");
    }
  }
  const s = state.series;
  const i = state.meta.dataset.years.indexOf(state.year);
  const u = s?.uncertainty?.[i];
  if (r?.meta.from) {
    $("pm").textContent = `since ${r.meta.from}`;
    $("pm").title = "Area-weighted mean change of the cells with data in both years";
  } else {
    $("pm").textContent = u == null ? "" : `±${u.toFixed(0)}`;
    $("pm").title = "Mean per-cell uncertainty (residual standard error) from the series' overview";
  }
  if (s?.error) out.push(s.error);
  notes(out, !!state.regionError);
}

function notes(list, error = false) {
  $("note").hidden = !list.length;
  $("note").textContent = list.join(" ");
  $("note").classList.toggle("error", error);
}

function mass(mg) { // Mg is tonnes
  const units = [[1e9, "Gt"], [1e6, "Mt"], [1e3, "kt"], [1, "t"]];
  const [div, unit] = units.find(([d]) => mg >= d) || units[3];
  return `${(mg / div).toPrecision(3)} ${unit}`;
}
function signed(v) { return `${v < 0 ? "−" : "+"}${Math.abs(v).toFixed(Math.abs(v) < 100 ? 1 : 0)}`; }
function percent(f) { return f >= 0.1 || f === 0 ? `${Math.round(f * 100)}%` : `${(f * 100).toPrecision(1)}%`; }
function area(ha) {
  const km2 = ha / 100;
  return km2 >= 1e6 ? `${(km2 / 1e6).toPrecision(3)} M km²` : `${km2.toLocaleString("en", { maximumSignificantDigits: 3 })} km²`;
}

function drawSeries() {
  const svg = $("spark");
  const s = state.series;
  if (!s || s.error || !s.agb) {
    svg.replaceChildren();
    svg.hidden = !!s?.error;
    return;
  }
  svg.hidden = false;
  // The axis spans the data, but never less than 20% of its peak, so a
  // forest that barely moved draws a nearly flat line.
  const W = 240, H = 44, n = s.years.length;
  const vals = s.agb.filter((v) => v != null);
  const peak = Math.max(...vals), span = Math.max(peak - Math.min(...vals), 0.2 * peak, 1);
  const mid = (peak + Math.min(...vals)) / 2;
  const x = (i) => (i / (n - 1)) * W;
  const y = (v) => H / 2 - ((v - mid) / span) * (H - 6);
  const pts = s.agb.map((v, i) => (v == null ? null : [x(i), y(v)])).filter(Boolean);
  const xi = x(s.years.indexOf(state.year));
  let period = "";
  if (state.mode === "change" && s.years.includes(state.from)) {
    const xf = x(s.years.indexOf(state.from));
    period = `<rect class="span" x="${Math.min(xf, xi)}" width="${Math.abs(xi - xf)}" y="0" height="${H}"/>` +
      `<line class="line" x1="${xf}" x2="${xf}" y1="0" y2="${H}" stroke-dasharray="2 2"/>`;
  }
  svg.innerHTML = period +
    `<polyline class="line" points="${pts.map((p) => p.join(",")).join(" ")}"/>` +
    `<line class="line" x1="${xi}" x2="${xi}" y1="0" y2="${H}" stroke-dasharray="2 2"/>`;
  svg.setAttribute("aria-label", `Mean biomass per year at ${s.level}: ${s.years[0]} ${fmt(s.agb[0])}, ${s.years[n - 1]} ${fmt(s.agb[n - 1])} t/ha`);
}
function fmt(v) { return v == null ? "no data" : v.toFixed(1); }

$("spark").addEventListener("click", (e) => {
  const s = state.series;
  if (!s?.years) return;
  const r = e.currentTarget.getBoundingClientRect();
  const i = Math.round(((e.clientX - r.left) / r.width) * (s.years.length - 1));
  $("year").value = s.years[Math.max(0, Math.min(s.years.length - 1, i))];
  $("year").dispatchEvent(new Event("input"));
});

// ── Colouring ───────────────────────────────────────────────────────────
function hex(c) { return [1, 3, 5].map((i) => parseInt(c.slice(i, i + 2), 16)); }
const STOPS = RAMP.map(([v, c]) => [v, hex(c)]);
function colour(v) { // t/ha → [r, g, b, a]
  if (v <= 0) return [0, 0, 0, 0];
  if (v >= STOPS[STOPS.length - 1][0]) return [...STOPS[STOPS.length - 1][1], 255];
  let i = 1;
  while (STOPS[i][0] < v) i++;
  const [v0, c0] = STOPS[i - 1], [v1, c1] = STOPS[i];
  const t = (v - v0) / (v1 - v0 || 1);
  const alpha = Math.min(1, v / 5) * 255;
  return [0, 1, 2].map((k) => c0[k] + (c1[k] - c0[k]) * t).concat(alpha);
}

const CHANGE_STOPS = { loss: [hex(LOSS_EDGE), hex(LOSS)], gain: [hex(GROWTH_EDGE), hex(GROWTH)] };
function changeColour(d, band) { // change in t/ha → [r, g, b, a]
  if (Math.abs(d) < band || d === 0) return [0, 0, 0, 0];
  const [c0, c1] = d < 0 ? CHANGE_STOPS.loss : CHANGE_STOPS.gain;
  const t = Math.min(1, (Math.abs(d) - band) / Math.max(1, CHANGE_END - band));
  return [0, 1, 2].map((k) => c0[k] + (c1[k] - c0[k]) * t).concat(255);
}

// The raster is on a longitude/latitude grid and the map is Web Mercator:
// an image source stretches linearly between its corners, so rows are
// resampled to be evenly spaced in Mercator y first.
function mercY(lat) { return Math.log(Math.tan(Math.PI / 4 + (lat * Math.PI) / 360)); }
function latOf(y) { return (360 / Math.PI) * Math.atan(Math.exp(y)) - 90; }

function render(meta, data) {
  const { rows, cols, decoding } = meta;
  const [w, s, e, n] = meta.bbox;
  const top = Math.min(n, MERCATOR_LIMIT), bottom = Math.max(s, -MERCATOR_LIMIT);
  const height = Math.min(2048, Math.ceil(rows * 1.5));
  const y0 = mercY(top), y1 = mercY(bottom), dLat = (n - s) / rows;

  // One colour per stored value. Biomass is 0…6000 (valid_min…valid_max),
  // a change −6000…6000, so the table starts at `lo`.
  const lo = meta.from ? -6000 : 0, hi = 6000;
  const lut = new Uint32Array(hi - lo + 1);
  const rgba = new Uint8Array(lut.buffer);
  for (let v = lo; v <= hi; v++) {
    const value = v * decoding.scale_factor + decoding.add_offset;
    rgba.set(meta.from ? changeColour(value, meta.summary.band) : colour(value), (v - lo) * 4);
  }

  const canvas = document.createElement("canvas");
  canvas.width = cols;
  canvas.height = height;
  const ctx = canvas.getContext("2d");
  const img = ctx.createImageData(cols, height);
  const px = new Uint32Array(img.data.buffer);
  for (let i = 0; i < height; i++) {
    const lat = latOf(y0 + ((i + 0.5) / height) * (y1 - y0));
    const r = Math.min(rows - 1, Math.max(0, Math.floor((n - lat) / dLat)));
    const src = r * cols, dst = i * cols;
    for (let c = 0; c < cols; c++) {
      const v = data[src + c];
      px[dst + c] = v === decoding.fill_value ? 0 : lut[Math.max(lo, Math.min(hi, v)) - lo];
    }
  }
  ctx.putImageData(img, 0, 0);
  canvas.toBlob((blob) => {
    const url = URL.createObjectURL(blob);
    map.getSource("region").updateImage({ url, coordinates: [[w, top], [e, top], [e, bottom], [w, bottom]] });
    setTimeout(() => URL.revokeObjectURL(url), 5000);
  });
}

// ── Hover readout ───────────────────────────────────────────────────────
map.on("mousemove", (e) => {
  const r = state.region, tip = $("tip");
  if (!r || start) return (tip.hidden = true);
  const { rows, cols, decoding, units } = r.meta;
  const [w, s, east, n] = r.meta.bbox;
  const { lng, lat } = e.lngLat;
  if (lng < w || lng >= east || lat <= s || lat > n) return (tip.hidden = true);
  const row = Math.floor(((n - lat) / (n - s)) * rows), col = Math.floor(((lng - w) / (east - w)) * cols);
  const v = r.data[row * cols + col];
  const value = v * decoding.scale_factor + decoding.add_offset;
  tip.textContent = v === decoding.fill_value ? "no data"
    : r.meta.from ? `${signed(value)} t/ha since ${r.meta.from}` : `${value.toFixed(1)} t/ha`;
  tip.title = units;
  tip.style.left = `${e.originalEvent.clientX + 12}px`;
  tip.style.top = `${e.originalEvent.clientY + 12}px`;
  tip.hidden = false;
});
map.on("mouseout", () => ($("tip").hidden = true));
