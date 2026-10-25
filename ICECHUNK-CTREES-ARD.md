# Icechunk, icechunk-go, and CTrees' ARD branches

This guide covers four things:

- what Icechunk is and why it suits distributed readers and writers;
- how your Go port is put together;
- what the CTrees biomass data actually holds;
- what the "ARDs" Naomi showed you at CUGOS are.

Every fact about the CTrees repository below was read from the live repository on 2026-10-25 with `icechunk-go`. That covers branch tips, commit messages, attributes, shapes, chunk counts and byte sizes. External claims link to their sources. Where nobody has published the reason for something, the guide says it is inferring the reason.

The working example of everything here is the map at
[icechunk.geographyart.com](https://icechunk.geographyart.com) (code in this
directory; see [README.md](README.md)).

---

## 1. The short answer: what is an ARD?

**ARD stands for Analysis-Ready Data.** The term has two meanings, and CTrees uses the second one.

**1. The satellite-agency meaning (CEOS-ARD, formerly CARD4L).** The Committee on Earth Observation Satellites defines ARD as

> satellite data that have been processed to a minimum set of requirements and organized into a form that allows immediate analysis with a minimum of additional user effort and interoperability both through time and with other datasets.
> — [CEOS CARD4L definition](https://ceos.org/document_management/Virtual_Constellations/LSI/CARD4L%20(CEOS%20Analysis%20Ready%20Data%20for%20Land)/CARD4L%20Definition/CARD4L_Definition%20Final.pdf)

In practice this means:

- calibrated to physical quantities;
- orthorectified, so that pixels line up through time;
- with per-pixel quality flags (cloud, shadow) and lineage metadata.

CEOS publishes product-family specifications for each kind of product: surface reflectance, surface temperature, SAR and others ([ceos.org/ard](https://ceos.org/ard/)). Agencies self-assess against them. Landsat Collection 2 surface reflectance is the classic example.

**2. The cloud-native meaning (ARCO: Analysis-Ready, Cloud-Optimized).** The Pangeo community stretched "analysis-ready" from satellite scenes to whole datasets: one coherent cube you *open*, instead of thousands of granule files you loop over. They paired it with "cloud-optimized" formats that object storage can serve in pieces ([Abernathey et al. 2021, *CiSE*](https://par.nsf.gov/servlets/purl/10287683)). Zarr is that format, and Icechunk adds transactions to it.

**CTrees means the second.** The DOI that Earthmover registered for this dataset gives its type as *"Analysis-ready, cloud-optimized Zarr / Icechunk data cube"* ([DataCite record](https://api.datacite.org/dois/10.82924/7vmb-zv66)). CTrees makes no claim to CEOS-ARD.

In the repository, "ARD" is two concrete changes, each on a branch:

| Branch | What it adds over `main` | Why it makes the data "analysis-ready" |
| --- | --- | --- |
| `ard` | One commit, "adding real scale_factor": the CF `scale_factor` goes from `1.0` to `0.1` on `agb` and `uncertainty`. | Any CF-aware reader (xarray, GDAL, rioxarray) now gets Mg/ha without having to know CTrees' private rule "divide by 10". |
| `ard-overviews` | `ard`, plus a pyramid of 5x, 25x, 125x and 625x mean-resampled copies, described by the `multiscales`, `spatial:` and `proj:` Zarr conventions. | You can read a continent or the globe cheaply. Tools can discover the levels and the georeferencing from metadata instead of from a README. |

No public document states why these branches exist, so the "why" column is inferred. The inference rests on:

- the commits themselves;
- CTrees' own tutorial, which tells readers to *"Apply the scale factor (e.g., divide by 10)"* by hand ([open-data-examples](https://github.com/ctrees-products/open-data-examples));
- Earthmover's tile server, which "automatically picks the overview level" of multiscale datasets ([docs](https://docs.earthmover.io/compute/tiles));
- Earthmover's open overview-building tool, whose defaults (`{factor}x` group names, `mean` resampling) match CTrees' pyramid exactly ([icechunk-multiscales-demo](https://github.com/earth-mover/icechunk-multiscales-demo)).

Naomi Provost speaks on this at FOSS4G-NA on 2026-11-03 ("Moving from Science to Product", [abstract](https://talks.osgeo.org/foss4g-na-2026/talk/K9BXMA/)). That talk is the place to confirm the intent.

So in this context, **an ARD is a branch of the same Icechunk repository whose metadata is fixed up so that ordinary tools read it correctly, at any scale, with no folklore.** It is not a different dataset.

---

## 2. Icechunk in one page

### The problem

Zarr splits an N-dimensional array into chunks, one object per chunk, plus a `zarr.json` metadata document. On object storage that gives you parallel I/O for free, but nothing ties those objects together:

- a reader can see half of a writer's update;
- two writers can clobber each other;
- there is no history.

Icechunk adds a transaction layer on top of Zarr, inspired by Apache Iceberg ([spec](https://github.com/earth-mover/icechunk/blob/main/icechunk-python/docs/docs/reference/spec-v2-1.md)).

### The layout

```
$ROOT/repo                  the ONLY mutable object: branches, tags, ancestry,
   │                        config, status, ops log (spec v2)
   ▼
snapshots/<id>              the whole tree at one moment: groups, arrays, their
   │                        zarr.json, and which manifests hold their chunks
   ▼
manifests/<id>              chunk index: (array, coords) → where the bytes are
   │
   ▼
chunks/<id>                 the compressed chunk bytes (or inline, or "virtual":
                            a URL + offset + length into someone else's file)
```

Every id is a random Crockford base32 string, and **every object except `repo` is written once and never changed**. A commit does four things:

1. writes new chunks;
2. writes new manifests for the arrays it touched;
3. writes a transaction log and a new snapshot;
4. swaps `repo` with a **conditional write**, which only succeeds if `repo` still has the ETag the writer read.

If another writer got there first, the conditional write fails. The loser can **rebase**: the transaction logs say which nodes and chunks each commit touched, so non-overlapping changes are replayed onto the new tip. Readers never lock, and they see a snapshot either entirely or not at all.

This is why it works so well for your distributed Cloudflare workers. Writers coordinate through a single compare-and-swap on one small file. Readers can cache every other object forever, because content at a given id never changes.

### Branches, tags, snapshots

- A **snapshot id** names an immutable state, like a git commit hash. Pinning one makes an analysis reproducible.
- A **branch** is a mutable pointer that moves with each commit.
- A **tag** is an immutable pointer.

Icechunk 2 (released 2026-04-08, now at 2.3.0) puts all refs, ancestry and config into the single `repo` file ([announcement](https://www.earthmover.io/blog/announcing-icechunk-2-better-consistency-performance-and-reliability-for-tensor-storage)). The CTrees repository is spec version 2, last written by icechunk 2.3.0-alpha.1.

### Concurrency you can see in CTrees' history

The `ard-overviews` branch was built on 2026-09-28 between 19:00 and 20:18 UTC. Each level got three kinds of commit:

1. an `initialise` commit that created the level's arrays;
2. **52 commits written in parallel**, one per variable and year ("overviews aboveground_biomass 5x agb t=6");
3. a `metadata` commit.

That is 208 parallel commits across the four levels. Their commit metadata records the rebases Icechunk performed: `"__icechunk": {"rebase_attempts": 42}` on the busiest. That is optimistic concurrency doing its job, on someone else's production data. Your own CLI shows it:

```sh
icechunk-go log -ref ard-overviews s3://ctrees-agb-100m-global/agb_100m_global | head
```

---

## 3. The CTrees dataset

### What it measures

**Aboveground biomass (AGB)** is the dry mass of living vegetation above the ground, mostly tree trunks, branches and leaves. It is given in megagrams per hectare (Mg/ha, which equals tonnes per hectare). Roughly half of it is carbon: the Earth Engine catalogue uses ×0.5 for carbon and ×3.667 on top of that for CO₂ ([catalogue](https://gee-community-catalog.org/projects/ctrees_global/)). The dataset gives one global map per year from 2000 to 2025, on a 1/1125° grid (≈99 m at the equator), plus a per-pixel **uncertainty**.

It is a model estimate, not a measurement. According to the preprint ([Yang et al. 2026, EarthArXiv](https://eartharxiv.org/repository/view/12941/), **not peer reviewed**):

- **Model:** a DenseNet convolutional network.
- **Training labels:** airborne-lidar biomass maps, field plots, national forest inventories and mangrove samples.
- **Predictors:** the Landsat time series, ALOS PALSAR L-band radar, GEDI and ICESat-2 canopy heights where available, and topography.
- **Validation** on held-out samples: R² 0.741, RMSE 59.5 Mg/ha, bias −4.82 Mg/ha.

The authors say that validation is per sample, not by spatial block. They also say block validation is needed before the numbers are used for formal reporting. CTrees' December 2025 data brief describes an older LightGBM method; treat the preprint as current.

The licence is CC BY 4.0, and the bucket is in the [AWS Open Data registry](https://registry.opendata.aws/ctrees-agb-100m-global/). Your earlier work on this data is in `zarro/plans/biomass/PLAN.md` §4. It found that fire losses are booked over two to three years and that undisturbed forest moves less than ±5 t/ha year to year.

### What is in the repository

`s3://ctrees-agb-100m-global/agb_100m_global`, region us-west-2, anonymous reads, group `aboveground_biomass`:

| Array | Shape | Type | Chunks | Notes |
| --- | --- | --- | --- | --- |
| `agb` | 26 × 202,500 × 405,000 | int16 | 1 × 2000 × 2000 | fill −9999, `valid_max` 6000, stored = Mg/ha × 10 |
| `uncertainty` | same | int16 | same | "Residual Standard Error", stored × 10 |
| `time` | 26 | datetime64[D] | 26 | 2000-01-01 … 2025-01-01 |
| `x`, `y` | 405,000 / 202,500 | float64 | 50,625 | cell centres, EPSG:4326 |

Some numbers worth knowing:

- **2.1 trillion cells per array.** Uncompressed that is 4.3 TB per array, but only **164,892 of the 538,356 possible chunks exist**. The rest is ocean or no data, never written, and read back as the fill value.
- **Stored size:** `agb` is **606 GB** and `uncertainty` 573 GB, zstd-compressed. A full-resolution chunk averages 3.7 MB.
- **Uncertainty** is per pixel. It grows with biomass, because optical and radar signals saturate in dense forest ([GEE catalogue](https://gee-community-catalog.org/projects/ctrees_global/)). It is *not* the error of a regional total: pixel errors are correlated, so they do not shrink when averaged. That is why the map shows "±" as a typical per-pixel figure, not as an error bar on the total. A precise published definition of "residual standard error" here could not be found.

### History

| When | What happened |
| --- | --- |
| 2025-02 → 2025-07 | Repository initialised. Experiments in groups `BRA.14_1` and `postprocessed` (their stale consolidated metadata still sits in the root `zarr.json`; plain Zarr readers trip on it, Icechunk ignores it). |
| 2026-04-08 | The global yearly GeoTIFFs are loaded, one commit per year. |
| 2026-04-15 | Tag **v1.0.0**, "adding AGB scale factor": the custom `agb_scale_factor: 10` attribute. |
| 2026-05-28 | Tag **v1.1.0**, "Fix time coordinate". |
| 2026-08-25 → 26 | Each year's uncertainty rewritten from GeoTIFF, and `agb` masked wherever uncertainty is nodata (26 commits). |
| 2026-08-29 | `main` = tag **v1.2.0** (`BK5RSJ8VDZF7JN5QBZHG`). `ard` branches off it with "adding real scale_factor" (`MPG277GVSBK7NHFXECWG`). |
| 2026-09-28 | `ard-overviews` (`Q0GD2YB5Y727KYMGCW70`) adds the pyramid. |

The ARD branches are not tagged. If you need reproducibility, pin the snapshot id, as this map does.

---

## 4. The ARD branches, concretely

### 4.1 `ard`: decoding without folklore

CF's packed-data rule ([CF 1.11](https://cfconventions.org/Data/cf-conventions/cf-conventions-1.11/cf-conventions.html), "Packed Data") is:

```
value = stored × scale_factor + add_offset
```

xarray applies it by default. On `main`, `agb` says this:

```json
"units": "Mg ha-1", "scale_factor": 1.0, "agb_scale_factor": 10, "_FillValue": -9999
```

`agb_scale_factor` is an attribute only CTrees' documentation explains. A generic reader applies `scale_factor: 1.0`, labels the result Mg/ha, and is ten times too high. On `ard`, `scale_factor` is `0.1`, and the numbers come out right without anyone having read anything:

```python
import icechunk, xarray as xr
repo = icechunk.Repository.open(icechunk.s3_storage(
    bucket="ctrees-agb-100m-global", prefix="agb_100m_global", region="us-west-2", anonymous=True))
for ref in ["main", "ard"]:
    ds = xr.open_zarr(repo.readonly_session(ref).store, group="aboveground_biomass", consolidated=False)
    print(ref, float(ds.agb.sel(time="2025").sel(x=slice(-60.2, -60.1), y=slice(-5.1, -5.2)).mean()))
# main 3065.1  ← "Mg/ha", ten times too high
# ard  306.5      (icechunk 2.1.2, xarray 2026.7, run 2026-10-25)
```

The map's Worker gets the same answer for that box in the Amazon: 3066.9 on `main` and 306.7 on `ard`. Its figures are area-weighted over a window snapped out to whole cells. Pick `main` in the map's branch menu and it shows the wrong numbers deliberately, with a note explaining why. In Go, [geozarr-go](https://github.com/solomonsealed/geozarr-go) works out a variable's CF decoding the way xarray does, and [`biomass.Decoding`](biomass/biomass.go) passes on the `scale_factor`, `add_offset` and `_FillValue` it finds.

This is what CEOS means by "calibrated to physical quantities", applied at the metadata level. The bytes on `ard` are identical to the bytes on `main`: the commit changed two `zarr.json` documents and no chunks.

### 4.2 `ard-overviews`: a pyramid inside the repository

| Level | Shape (y × x) | Chunks | Cell at equator | Stored `agb` | `agb` manifest |
| --- | --- | --- | --- | --- | --- |
| `.` (1x) | 202,500 × 405,000 | 2000² | 99 m | 606 GB, 164,892 chunks | 3.8 MB |
| `5x` | 40,500 × 81,000 | 512² | 495 m | 24.6 GB, 105,508 | 2.8 MB |
| `25x` | 8,100 × 16,200 | 512² | 2.5 km | 1.0 GB, 6,604 | 190 KB |
| `125x` | 1,620 × 3,240 | 512² | 12 km | 45 MB, 546 | 13 KB |
| `625x` | 324 × 648 | 512² | 62 km | 2 MB, 52 | 1.3 KB |

Each level is the **mean** of 5×5 cells of the level below it (`derived_from` and `resampling_method` in the metadata). The whole `agb` pyramid costs about 4% of the full-resolution array. This project's live test, [`TestLiveOverviewIsMeanOfCells`](biomass/live_test.go), checks every 5x cell in a box against the mean of its 25 full-resolution cells; they agree to within 0.1 Mg/ha.

The group's attributes describe all of this with three [Zarr conventions](https://github.com/zarr-conventions). A convention is a set of attributes that plain Zarr libraries ignore and specialised tools understand, registered in a `zarr_conventions` list.

- **`multiscales`**: a `layout` with one entry per level, giving:
  - `asset`: the subgroup, `.` for the group itself;
  - `derived_from`: the level it was computed from;
  - `transform`: scale 5, translation 2, so the coarse cell centre sits over the middle of its 5×5 block;
  - `resampling_method`;
  - the absolute `spatial:shape` and `spatial:transform` of the level.
- **`spatial:`**: the affine `transform` `[a, b, c, d, e, f]` in rasterio order (lon = c + a·col, lat = f + e·row), `shape`, `bbox` and `dimensions`.
- **`proj:`**: `proj:code` `EPSG:4326` and the WKT2.

All three are still **Pilot** maturity (tagged v0.1 on GitHub as of today). CTrees' schema URLs point at `v1` tags and at the older `zarr-experimental/geo-proj` name, so expect some churn. Readers that already understand them include:

- GDAL's Zarr driver (3.13), and an Icechunk driver in GDAL 3.14 ([docs](https://gdal.org/en/latest/drivers/raster/icechunk.html));
- rioxarray (0.22+);
- rio-tiler and TiTiler;
- Earthmover's xpublish-tiles;
- CarbonPlan's zarr-layer for MapLibre (experimental).

The map reads the levels from this metadata with geozarr-go instead of hard-coding them, and finds each level's grid as rioxarray does: from the `spatial_ref` grid mapping's `GeoTransform` at full resolution, and from the `x_5x`/`y_5x`… coordinates on the overviews. Branches without the metadata have one level, the group's own grid ([`biomass.Open`](biomass/biomass.go)).

**Why this matters for you.** Without overviews, reading Brazil means decoding hundreds of 2000² chunks. With them, the map always reads at most about a 1024² window at whichever level fits:

- the whole world at 625x is one read of two chunks, about 0.2 s in the Worker;
- a 26-year series over a region reads a coarse level and takes about half a second.

Switch the map to `ard` or `main` and large boxes are refused ("1125 cells across at 1x"), and there is no time series. That contrast is the whole argument for overviews.

This is what Ian meant in the GitHub thread: overviews make reading straight from Icechunk viable for exploratory maps, where you went through PMTiles before. PMTiles are still the better tool for a global, styled, zoomable basemap. You pay for them once at build time, and serving is a byte-range lookup. Overviews are better when the user picks the region, or wants the actual numbers, a GeoTIFF or a time series.

### 4.3 Which ref to use

- **`ard-overviews`**: anything visual, regional or interactive. It is also a superset of `ard`, so full-resolution analysis works on it too.
- **`ard`**: full-resolution analysis without the pyramid.
- **`main` and tags v1.x**: only for reproducing older work. Remember to divide by 10.
- **A snapshot id**: whenever results must be reproducible. Branches move; ids do not.

A related open proposal in CTrees' own toolkit ([ctreeskit#33](https://github.com/ctrees-products/ctreeskit/issues/33), opened 2026-10-09) asks to describe overview branches with these same conventions. The conventions are still settling.

---

## 5. icechunk-go: how the port is put together

`github.com/solomonsealed/icechunk-go` is a pure-Go reader and writer for Icechunk spec 1, 2 and 2.1. Arrays come from `github.com/solomonsealed/zarr-go`. It builds for `GOOS=js GOARCH=wasm`; this project's Worker, with geozarr-go on top, is 2.9 MB gzipped.

### The layers, bottom to top

1. **`storage.Storage`**: one method, `Get(ctx, key, *GetOptions)`, with byte ranges and `If-Match` / `If-Unmodified-Since`. Writers add conditional `Put`. Implementations:
   - local files and memory;
   - `httpstore` for HTTP and SigV4 S3 (AWS, R2, MinIO, GCS interop);
   - `cfworker` for R2 bindings and `fetch`.
2. **`Repository`**. It reads `repo` and caches it for `RepoInfoTTL`. It decodes snapshots and manifests (FlatBuffers) and caches them up to `CacheBytes`. Manifests are searched in place in the FlatBuffer, never expanded into Go structs. That is why a 3.8 MB manifest fits comfortably in a Worker.
3. **`Session`**: one snapshot, opened `AtBranch`, `AtTag` or `AtSnapshot`. `OpenArray` returns a `*zarr.Array` whose chunks come from the session's manifests.
4. **zarr-go `Array`**: `Read(start, count)`, NumPy-style `Get`, codecs and sharding. The result is an `NDArray`: little-endian bytes plus a shape. The Worker sends those bytes to the browser untouched.

On top of a `Session`, this project uses [geozarr-go](https://github.com/solomonsealed/geozarr-go). It opens the group as `xarray.open_zarr` does, reads the multiscales layout, finds each level's CRS and transform as rioxarray does, and selects a box's cells as `clip_box` does. Its `icechunksrc` package reads all of that from a session.

The core of this project's read path ([`biomass.go`](biomass/biomass.go)):

```go
st := cfworker.NewS3(storage.S3Config{Bucket: "ctrees-agb-100m-global", Prefix: "agb_100m_global", Region: "us-west-2", Anonymous: true})
repo, _ := icechunk.Open(ctx, st, &icechunk.Options{CacheBytes: 32 << 20, RepoInfoTTL: time.Minute})
s, _ := repo.ReadonlySession(ctx, icechunk.AtBranch("ard-overviews"))
ds, _ := icechunksrc.Open(ctx, s, "aboveground_biomass") // as xarray.open_zarr
ms, _ := ds.Multiscales()                                 // the layout: ., 5x, 25x, 125x, 625x
level, _ := ds.OpenLevel(ctx, ms.Layout[2])               // 25x
grid, _ := level.Grid(ctx, "agb")                         // EPSG:4326 and its transform, as rioxarray
w, _ := grid.ClipBox(west, south, east, north, nil)       // the box's cells, as clip_box
agb := level.Variable("agb")                              // agb.Encoding: scale_factor 0.1, _FillValue -9999
nd, _ := agb.Array.Read(ctx,
	[]uint64{25, uint64(w.RowStart), uint64(w.ColStart)},                     // 2025
	[]uint64{1, uint64(w.RowStop - w.RowStart), uint64(w.ColStop - w.ColStart)})
vals, _ := zarr.Convert[int16](nd)
```

The values are read as stored, not decoded: int16 is a quarter of the float64 xarray would decode them to, and the browser applies the decoding itself.

### How it stays honest

The port is checked against upstream rather than trusted. Upstream's own compatibility fixtures, plus repositories written by icechunk-python 2.3.0, must read byte-for-byte the same. An oracle records everything icechunk-python reads, and the Go reader and the HTTP service must match it. The writer is checked by:

- scenarios run with both writers and compared;
- icechunk-python verifying Go-written repositories and committing on top of them.

Details are in the port's README.

The port leaves maintenance to the official library: garbage collection, expiration, config changes and v1 upgrades.

### Keeping up with upstream (your question to Ian)

There is no newsletter, but three things come close:

1. **Watch releases.** On [earth-mover/icechunk](https://github.com/earth-mover/icechunk), choose Watch → Custom → Releases. GitHub will email you each release.
2. **Use the conformance suite as the upgrade procedure.** Bump icechunk-python in `testdata/requirements.txt` and regenerate fixtures and oracles. Then run `TestPython*` and `TestWriterScenarios`. What fails is your to-do list. `icechunk.CompatibleVersion` records which release the port matches.
3. **Follow [PR #2477](https://github.com/earth-mover/icechunk/pull/2477)** (open as of 2026-10-24), which Ian flagged. It adds safe repositories on shared filesystems (NFS, Lustre) and introduces spec 2.2 **writer features**:
   - The first writer feature is a `repo.d/` layout of immutable repo-info versions.
   - Repositories that use a writer feature record their status as `ReadOnly`, with the real status kept separately. Icechunk 2.0–2.3 can therefore still read them but refuse to write.
   - icechunk-go already refuses every repo-info update unless the status is online (`commit.go`), so it should degrade the same safe way. Add a fixture for this once the PR lands.

### Alternatives you weighed

- **Python**: the official library, with the whole xarray, dask and rioxarray ecosystem.
- **Rust**: [zarrs](https://zarrs.dev) with [zarrs_icechunk](https://github.com/zarrs/zarrs_icechunk) (0.6.0 targets Icechunk 2.3).
- **GDAL 3.14's Icechunk driver**: for anything that already speaks GDAL.

The Go port's niche is the one this map uses: a small, readable, single-binary reader and writer that runs in a Cloudflare Worker next to your other Go code.

---

## 6. How the map at icechunk.geographyart.com works

```
browser ──/api/meta?ref=ard-overviews──▶ Worker (Go → WebAssembly)
        ◀── snapshot Q0GD2YB5…, levels, years, CF decoding
browser ──/api/region?ref=Q0GD2YB5…&bbox=…&year=2025──▶ Worker
        │   picks the finest level whose window is ≤ 1024 cells across
        │   geozarr-go: the level's grid → the box's cells
        │   icechunk-go: repo → snapshot → manifest → chunks  ──fetch──▶ S3 us-west-2
        ◀── raw int16 + {level, bounds, decoding, area-weighted summary}
browser ──/api/series?…──▶ Worker: agb and uncertainty, all 26 years, at a
        ◀── coarse level (≥ 16 cells across), area-weighted means
```

The map opens on **change**: `&from=2000` makes the Worker read both years
over the same window and return `year − from`, with the area that lost or
gained at least 10 t/ha. The two reads run one after the other, since two
full-resolution reads at once would not fit in an isolate's 128 MB. Red is
loss and blue is gain, as on the World Biomass Map, and changes within
±10 t/ha are left clear. That band is not arbitrary: your PLAN.md §4.2
measured that undisturbed forest moves less than ±5 t/ha from one year to
the next. Remember that CTrees books a fire's loss over two to three years,
so 2017→2019 shows the Camp Fire better than 2018→2019.

Four design points:

- **Snapshot ids make caching trivial.** The page resolves its branch once, then asks for everything by snapshot id. Those responses never change, so they are cached for a year. A repeated request is an edge-cache hit and never runs Go.
- **S3 reads are cached at the edge too.** `worker.mjs` wraps `fetch`: every repository object except `repo` is immutable, so Cloudflare keeps it for a year, and `repo` for a minute. That is Icechunk's design paying off directly, and it is also kind to CTrees' egress.
- **Attribution.** Every S3 request carries `User-Agent: geographyart-icechunk/1 icechunk/2.3.0 (workload=icechunk.geographyart.com; array=…; chunk=…)`, so CTrees can see who reads their bucket in their logs.
- **Projection.** The data is on a longitude/latitude grid and MapLibre draws Web Mercator. The browser resamples rows before placing the image, so the cells sit where they belong, even in tall boxes.

Measured on the staging Worker:

| Read | Time in the Worker |
| --- | --- |
| A 113² full-resolution box (cold) | 0.57 s |
| A 1013² full-resolution box, touching four 2000² chunks | 0.41 s |
| The world at 625x | 0.20 s |
| A 26-year series | 0.5–0.6 s |
| A cached repeat | a few milliseconds |

---

## 7. Glossary

| Term | Meaning |
| --- | --- |
| **AGB** | Aboveground biomass: dry mass of living vegetation above ground, Mg/ha (= t/ha). |
| **ARD** | Analysis-ready data. CEOS: satellite data processed to a standard for immediate analysis. Here: a branch fixed up so that standard tools read correct values at any scale. |
| **ARCO** | Analysis-ready, cloud-optimized: a whole dataset as one cube in a chunked cloud format. |
| **CF conventions** | Climate and Forecast metadata conventions, including `scale_factor`, `add_offset` and `_FillValue` for packed data. |
| **Chunk / shard** | The unit Zarr stores and compresses. A shard packs many chunks into one object (not used here). |
| **Manifest** | Icechunk's index from (array, chunk coordinates) to bytes. |
| **Snapshot** | An immutable state of the whole repository. Its id is the reproducible reference. |
| **Branch / tag** | Mutable and immutable names for snapshots. |
| **Rebase** | Replaying a commit onto a branch tip that moved, when the changes do not overlap. |
| **Overview / multiscale** | A coarser, resampled copy of an array, here the mean of 5×5 cells per level. |
| **Fill value** | The stored value meaning "no data": −9999 here; ocean and absent chunks read as it. |

## 8. Sources

- CTrees repository: `s3://ctrees-agb-100m-global/agb_100m_global`, read with `icechunk-go` (`info`, `log`, `ls`, `cat`, `chunks`), 2026-10-25.
- DataCite record for the dataset DOI: https://api.datacite.org/dois/10.82924/7vmb-zv66
- AWS Open Data registry: https://registry.opendata.aws/ctrees-agb-100m-global/
- Yang et al. 2026, EarthArXiv preprint: https://eartharxiv.org/repository/view/12941/ (also doi:10.31223/X5KJ4Q)
- CTrees tutorial: https://github.com/ctrees-products/open-data-examples
- CEOS CARD4L definition: linked in §1; CEOS-ARD specifications: https://ceos.org/ard/
- Abernathey et al. 2021, "Cloud-Native Repositories for Big Scientific Data", *CiSE* 23(2)
- Icechunk spec 2.1 and Icechunk 2 announcement: linked in §2
- Zarr conventions: https://github.com/zarr-conventions/multiscales, …/spatial, …/proj
- Earthmover tiles and multiscales tooling: https://docs.earthmover.io/compute/tiles, https://github.com/earth-mover/icechunk-multiscales-demo
- Icechunk PR #2477: https://github.com/earth-mover/icechunk/pull/2477
- Your issue thread with Ian (ianhi on GitHub): https://github.com/solomonsealed/icechunk-go/issues/2
