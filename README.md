# CTrees biomass, read with icechunk-go

A minimal map at [icechunk.geographyart.com](https://icechunk.geographyart.com):
drag a box anywhere on Earth and see where biomass fell or grew. A Cloudflare
Worker written in Go reads
[CTrees' 100 m aboveground biomass](https://registry.opendata.aws/ctrees-agb-100m-global/)
for it straight from their public Icechunk repository on S3, with
[icechunk-go](https://github.com/solomonsealed/icechunk-go) and
[geozarr-go](https://github.com/solomonsealed/geozarr-go), which reads the
pyramid's levels, grids and CF decoding as xarray and rioxarray would. It
defaults to the `ard-overviews` branch and picks the pyramid level that fits
the box.

[ICECHUNK-CTREES-ARD.md](ICECHUNK-CTREES-ARD.md) explains Icechunk, the Go port,
the dataset, and what CTrees' ARD branches are.

Shift-drag (or the box button, then drag) draws a box. **Change** (the
default) colours each cell by how much its biomass changed between two years,
2000 and 2025 unless you pick others: red for loss, blue for gain, clear
within ±10 t/ha, full colour at ±150 t/ha, as on the World Biomass Map. The
card gives the mean change, the share of the area that lost or gained at
least 10 t/ha, and the net change in tonnes. **Biomass** shows one year: the
area-weighted mean in t/ha, the typical per-pixel uncertainty and the total.
Both show the mean for every year 2000–2025 and offer GeoTIFF downloads.
The branch menu switches between `ard-overviews`, `ard`, `main` and the tags;
`main` shows biomass ten times too high on purpose, which is what the `ard`
branches fix. The URL keeps the branch, mode, years and box.

| Path | What |
| --- | --- |
| [main.go](main.go) | The Worker (`GOOS=js GOARCH=wasm`): routes, ref resolution, caching headers |
| [biomass/](biomass/) | Levels from the `multiscales` layout and their grids (geozarr-go), level choice, windows (`clip_box`), reads, area-weighted stats, GeoTIFF encoder; native Go, tested |
| [worker.mjs](worker.mjs) | Starts Go, caches S3 objects and API responses in Cloudflare's cache |
| [public/](public/) | The page: MapLibre, one card, no framework |

## API

| Route | Returns |
| --- | --- |
| `GET /api/meta?ref=ard-overviews` | branches, tags, the ref's snapshot id, levels, years, CF decoding |
| `GET /api/region?ref=&bbox=w,s,e,n&year=2025` | one year over the box, at the finest level ≤ `MAX_CELLS` (1024) cells across: little-endian int16, with level, bounds, decoding and summary in the `x-region` header. `&var=uncertainty`, `&format=tif` |
| `GET /api/region?…&year=2025&from=2000[&band=10]` | the change from `from` to `year` instead (`year − from`, same encoding; fill unless valid in both years), with the area that lost or gained at least `band` t/ha and the net change in the summary |
| `GET /api/series?ref=&bbox=w,s,e,n` | mean `agb` and `uncertainty` for every year, from the coarsest overview ≥ 16 cells across; 422 on refs without overviews |

`ref` is a branch, tag or snapshot id. Responses for a snapshot id are cached
for a year; the page resolves its branch with `/api/meta` and then uses the id.

```sh
curl -o paradise.tif "https://icechunk.geographyart.com/api/region?ref=Q0GD2YB5Y727KYMGCW70&bbox=-121.7,39.7,-121.5,39.85&year=2019&format=tif"
```

## Develop

Needs Go ≥ 1.25 and Node.

```sh
npm install
npm test                                   # go test ./biomass
CTREES_LIVE=1 go test ./biomass -run Live  # reads the real repository on S3
npm run dev                                # build/app.wasm + wrangler dev on :8787
```

## Deploy

As in zarro, the default environment is staging:

| | Worker | Address |
| --- | --- | --- |
| `npm run deploy` | `geographyart-icechunk-staging` | https://icechunk-staging.geographyart.com |
| `npm run deploy:production` | `geographyart-icechunk` | https://icechunk.geographyart.com |

The Worker has no bindings or secrets; it reads the bucket anonymously, and
every S3 request names it in its `User-Agent` (`WORKLOAD`).

## Data

CTrees, *Global aboveground biomass 100 m, 2000–2025*, CC BY 4.0. Yang et al.
2026, EarthArXiv, https://doi.org/10.31223/X5KJ4Q.

## License

The code is MIT; see [LICENSE](LICENSE). The data is CTrees', under CC BY 4.0.
