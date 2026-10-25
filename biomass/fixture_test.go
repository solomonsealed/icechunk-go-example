package biomass

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	icechunk "github.com/solomonsealed/icechunk-go"
	"github.com/solomonsealed/icechunk-go/storage"
	zarr "github.com/solomonsealed/zarr-go"
)

// rootAttrs are the group's attributes on the ard-overviews branch
// (snapshot Q0GD2YB5Y727KYMGCW70), less proj:epsg, proj:wkt2 and
// spatial:bbox.
const rootAttrs = `{
  "zarr_conventions": [
    {"name": "multiscales", "uuid": "d35379db-88df-4056-af3a-620245f8e347",
     "schema_url": "https://raw.githubusercontent.com/zarr-conventions/multiscales/refs/tags/v1/schema.json",
     "spec_url": "https://github.com/zarr-conventions/multiscales/blob/v1/README.md"},
    {"name": "spatial:", "uuid": "689b58e2-cf7b-45e0-9fff-9cfc0883d6b4",
     "schema_url": "https://raw.githubusercontent.com/zarr-conventions/spatial/refs/tags/v1/schema.json",
     "spec_url": "https://github.com/zarr-conventions/spatial/blob/v1/README.md"},
    {"name": "proj:", "uuid": "f17cb550-5864-4468-aeb7-f3180cfb622f",
     "schema_url": "https://raw.githubusercontent.com/zarr-experimental/geo-proj/refs/tags/v1/schema.json",
     "spec_url": "https://github.com/zarr-experimental/geo-proj/blob/v1/README.md"}
  ],
  "multiscales": {"layout": [
    {"asset": ".", "spatial:shape": [202500, 405000],
     "spatial:transform": [0.0008888888888805013, 0, -180, 0, -0.0008888888888947122, 90],
     "transform": {"scale": [1, 1, 1], "translation": [0, 0, 0]}},
    {"asset": "5x", "derived_from": ".", "resampling_method": "mean", "spatial:shape": [40500, 81000],
     "spatial:transform": [0.004444444444430928, 0, -180, 0, -0.004444444444445139, 90],
     "transform": {"scale": [1, 5, 5], "translation": [0, 2, 2]}},
    {"asset": "25x", "derived_from": "5x", "resampling_method": "mean", "spatial:shape": [8100, 16200],
     "spatial:transform": [0.022222222222183063, 0, -180, 0, -0.022222222222183063, 89.99999999999997],
     "transform": {"scale": [1, 5, 5], "translation": [0, 2, 2]}},
    {"asset": "125x", "derived_from": "25x", "resampling_method": "mean", "spatial:shape": [1620, 3240],
     "spatial:transform": [0.11111111111097216, 0, -179.99999999999997, 0, -0.11111111111098637, 90],
     "transform": {"scale": [1, 5, 5], "translation": [0, 2, 2]}},
    {"asset": "625x", "derived_from": "125x", "resampling_method": "mean", "spatial:shape": [324, 648],
     "spatial:transform": [0.5555555555549461, 0, -180, 0, -0.5555555555549603, 90],
     "transform": {"scale": [1, 5, 5], "translation": [0, 2, 2]}}
  ]},
  "proj:code": "EPSG:4326",
  "spatial:dimensions": ["y", "x"],
  "spatial:shape": [202500, 405000],
  "spatial:transform": [0.0008888888888805013, 0, -180, 0, -0.0008888888888947122, 90]
}`

// testDataset opens an in-memory repository laid out as the ard-overviews
// branch: the group with its multiscales layout, a group per overview with
// its spatial: and proj: attributes, agb and uncertainty at every level,
// and the years. No chunks are written, and there are no x and y
// coordinates, so each level's transform is its spatial:transform.
func testDataset(t *testing.T) *Dataset {
	t.Helper()
	ctx := context.Background()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var root map[string]any
	check(json.Unmarshal([]byte(rootAttrs), &root))
	repo, err := icechunk.Create(ctx, storage.NewMemory(nil), nil)
	check(err)
	w, err := repo.WritableSession(ctx, "main")
	check(err)
	check(w.CreateGroup(ctx, "/", nil))
	check(w.CreateGroup(ctx, "/"+Group, root))

	days := make([]int64, 26)
	for i := range days {
		days[i] = time.Date(2000+i, 1, 1, 0, 0, 0, 0, time.UTC).Unix() / 86400
	}
	ta, err := w.CreateArray(ctx, "/"+Group+"/time", zarr.ArraySpec{Shape: []uint64{26}, DataType: "int64", DimensionNames: []string{"time"},
		Attributes: map[string]any{"units": "days since 1970-01-01", "calendar": "proleptic_gregorian"}})
	check(err)
	nd, err := zarr.FromSlice([]uint64{26}, days)
	check(err)
	check(ta.Write(ctx, []uint64{0}, nd))

	for _, e := range root["multiscales"].(map[string]any)["layout"].([]any) {
		e := e.(map[string]any)
		path, y, x := "/"+Group, "y", "x"
		if asset := e["asset"].(string); asset != "." {
			path, y, x = path+"/"+asset, "y_"+asset, "x_"+asset
			check(w.CreateGroup(ctx, path, map[string]any{
				"zarr_conventions":   root["zarr_conventions"].([]any)[1:],
				"proj:code":          "EPSG:4326",
				"spatial:dimensions": []any{y, x},
				"spatial:shape":      e["spatial:shape"],
				"spatial:transform":  e["spatial:transform"],
			}))
		}
		shape := e["spatial:shape"].([]any)
		rows, cols := uint64(shape[0].(float64)), uint64(shape[1].(float64))
		for _, v := range Variables {
			_, err := w.CreateArray(ctx, path+"/"+v, zarr.ArraySpec{
				Shape: []uint64{26, rows, cols}, ChunkShape: []uint64{1, min(rows, 2000), min(cols, 2000)},
				DataType: "int16", FillValue: int16(-9999), DimensionNames: []string{"time", y, x},
				Attributes: map[string]any{"_FillValue": -9999, "scale_factor": 0.1, "units": "Mg ha-1", "agb_scale_factor": 10},
			})
			check(err)
		}
	}
	_, err = w.Commit(ctx, "ard-overviews", nil)
	check(err)

	s, err := repo.ReadonlySession(ctx, icechunk.AtBranch("main"))
	check(err)
	d, err := Open(ctx, s)
	check(err)
	return d
}
