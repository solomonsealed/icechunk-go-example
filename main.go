//go:build js && wasm

// Command ctrees-icechunk is a Cloudflare Worker, written in Go, that reads
// CTrees' 100 m aboveground biomass straight from its public Icechunk
// repository on S3 with icechunk-go and geozarr-go, for whatever box a map
// user draws.
//
//	GET /api/meta?ref=ard-overviews
//	    branches, tags, the ref's snapshot, levels, years and CF decoding
//	GET /api/region?ref=…&bbox=west,south,east,north&year=2025[&var=agb|uncertainty][&format=tif]
//	    one year over the box at the finest level that fits in MAX_CELLS:
//	    raw little-endian int16 (shape, bounds and summary in x-region),
//	    or a GeoTIFF
//	    &from=2000[&band=10]: the change from that year instead, with the
//	    area that lost or gained at least band (t/ha) in the summary
//	GET /api/series?ref=…&bbox=…
//	    the mean of agb and uncertainty over the box, every year, read from
//	    a coarse overview
//
// ref is a branch, a tag or a snapshot id. Responses for a snapshot id never
// change and are cached for a year; the page resolves its branch to a
// snapshot through /api/meta first.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	icechunk "github.com/solomonsealed/icechunk-go"
	"github.com/solomonsealed/icechunk-go/cfworker"
	"github.com/solomonsealed/icechunk-go/storage"

	"github.com/solomonsealed/ctrees-icechunk-go-example/biomass"
)

// The repository handle lives as long as the isolate, so decoded snapshots
// and manifests are cached across requests.
var repo atomic.Pointer[icechunk.Repository]

// datasets holds a *biomass.Dataset per snapshot id.
var datasets sync.Map

// repository returns the shared repository, opening it on first use.
// Concurrent cold requests each open it rather than wait on one another: in
// a Worker, a request must never wait for I/O another request started.
func repository(ctx context.Context, env cfworker.Env) (*icechunk.Repository, error) {
	if r := repo.Load(); r != nil {
		return r, nil
	}
	st := cfworker.NewS3(storage.S3Config{
		Bucket:    env.Var("REPO_BUCKET"),
		Prefix:    env.Var("REPO_PREFIX"),
		Region:    env.Var("REPO_REGION"),
		Anonymous: true,
	})
	r, err := icechunk.Open(ctx, st, &icechunk.Options{
		// Isolates have 128 MB. The largest manifests here are 3.8 MB.
		CacheBytes:  32 << 20,
		RepoInfoTTL: 60 * time.Second,
		Concurrency: 8,
		// Names this service in the User-Agent of every S3 request, so
		// CTrees can see in their access logs who reads their bucket.
		Attribution: icechunk.Attribution{Client: "geographyart-icechunk/1", Workload: env.Var("WORKLOAD")},
	})
	if err != nil {
		return nil, err
	}
	repo.CompareAndSwap(nil, r)
	return repo.Load(), nil
}

// session resolves ref as a branch, then a tag, then a snapshot id.
func session(ctx context.Context, r *icechunk.Repository, ref string) (s *icechunk.Session, pinned bool, err error) {
	s, err = r.ReadonlySession(ctx, icechunk.AtBranch(ref))
	if !errors.Is(err, icechunk.ErrRefNotFound) {
		return s, false, err
	}
	s, err = r.ReadonlySession(ctx, icechunk.AtTag(ref))
	if !errors.Is(err, icechunk.ErrRefNotFound) {
		return s, false, err
	}
	if id, perr := icechunk.ParseSnapshotID(ref); perr == nil {
		s, err = r.ReadonlySession(ctx, icechunk.AtSnapshot(id))
		return s, true, err
	}
	return nil, false, httpError{404, fmt.Sprintf("no branch, tag or snapshot %q", ref)}
}

func dataset(ctx context.Context, s *icechunk.Session) (*biomass.Dataset, error) {
	id := s.SnapshotID().String()
	if d, ok := datasets.Load(id); ok {
		return d.(*biomass.Dataset), nil
	}
	d, err := biomass.Open(ctx, s)
	if err != nil {
		return nil, err
	}
	v, _ := datasets.LoadOrStore(id, d)
	return v.(*biomass.Dataset), nil
}

type httpError struct {
	status int
	msg    string
}

func (e httpError) Error() string { return e.msg }

func main() {
	// Go's heap grows to twice what is live before collecting; a limit
	// under the isolate's 128 MB makes it collect sooner instead.
	debug.SetMemoryLimit(96 << 20)
	cfworker.Serve(func(ctx context.Context, r *cfworker.Request) (*cfworker.Response, error) {
		resp, err := handle(ctx, r)
		if err != nil {
			status := 500
			var he httpError
			var tl *biomass.TooLargeError
			switch {
			case errors.As(err, &he):
				status = he.status
			case errors.As(err, &tl):
				status = 413
			case errors.Is(err, biomass.ErrNoOverviews):
				status = 422
			}
			body, _ := json.Marshal(map[string]string{"error": err.Error()})
			return &cfworker.Response{Status: status, Body: body, Header: map[string]string{
				"content-type": "application/json", "cache-control": "no-store",
			}}, nil
		}
		return resp, nil
	})
}

func handle(ctx context.Context, r *cfworker.Request) (*cfworker.Response, error) {
	start := time.Now()
	if r.Method != "GET" && r.Method != "HEAD" {
		return nil, httpError{405, "GET only"}
	}
	q := r.URL.Query()
	ref := q.Get("ref")
	if ref == "" {
		ref = r.Env.Var("DEFAULT_REF")
	}
	rp, err := repository(ctx, r.Env)
	if err != nil {
		return nil, err
	}
	s, pinned, err := session(ctx, rp, ref)
	if err != nil {
		return nil, err
	}
	d, err := dataset(ctx, s)
	if err != nil {
		return nil, err
	}
	header := map[string]string{"cache-control": "public, max-age=60"}
	if pinned {
		header["cache-control"] = "public, max-age=31536000, immutable"
	}
	ms := func() float64 { return float64(time.Since(start).Microseconds()) / 1000 }

	switch r.URL.Path {
	case "/api/meta":
		out := map[string]any{"ref": ref, "dataset": d, "decoding": map[string]biomass.Decoding{}}
		for _, v := range biomass.Variables {
			out["decoding"].(map[string]biomass.Decoding)[v] = d.Decoding(v)
		}
		if out["branches"], err = refs(ctx, rp.ListBranches, rp.LookupBranch); err != nil {
			return nil, err
		}
		if out["tags"], err = refs(ctx, rp.ListTags, rp.LookupTag); err != nil {
			return nil, err
		}
		out["max_cells"] = maxCells(r.Env)
		return jsonResponse(header, out)

	case "/api/region":
		bbox, err := biomass.ParseBBox(q.Get("bbox"))
		if err != nil {
			return nil, httpError{400, err.Error()}
		}
		v := q.Get("var")
		if v == "" {
			v = "agb"
		}
		year := d.Years[len(d.Years)-1]
		if y := q.Get("year"); y != "" {
			if year, err = strconv.Atoi(y); err != nil {
				return nil, httpError{400, "year: " + err.Error()}
			}
		}
		w, err := biomass.ForImage(d.Levels, bbox, maxCells(r.Env))
		if err != nil {
			return nil, err
		}
		var raster *biomass.Raster
		var summary any
		if f := q.Get("from"); f != "" {
			from, err := strconv.Atoi(f)
			if err != nil {
				return nil, httpError{400, "from: " + err.Error()}
			}
			band := float64(biomass.MinChange)
			if b := q.Get("band"); b != "" {
				if band, err = strconv.ParseFloat(b, 64); err != nil || band < 0 {
					return nil, httpError{400, "band: want a number of t/ha ≥ 0"}
				}
			}
			if raster, err = d.ReadChange(ctx, w, v, from, year); err != nil {
				return nil, httpError{400, err.Error()}
			}
			summary = biomass.SummarizeChange(raster, band)
		} else {
			if raster, err = d.Read(ctx, w, v, year); err != nil {
				return nil, httpError{400, err.Error()}
			}
			summary = biomass.Summarize(raster.Values, w, raster.Decoding)
		}
		b := w.BBox()
		units, _ := d.Attrs[v]["units"].(string)
		if q.Get("format") == "tif" {
			tif, err := biomass.GeoTIFF(raster, units, map[string]string{
				"source":   "CTrees aboveground biomass 100 m, s3://ctrees-agb-100m-global/agb_100m_global (CC BY 4.0)",
				"ref":      ref,
				"snapshot": d.Snapshot,
				"level":    w.Level.Name,
				"year":     strconv.Itoa(year),
				"contents": raster.Title(),
			})
			if err != nil {
				return nil, err
			}
			header["content-type"] = "image/tiff"
			years := strconv.Itoa(year)
			if raster.From != 0 {
				years = fmt.Sprintf("change-%d-%d", raster.From, year)
			}
			header["content-disposition"] = fmt.Sprintf(`attachment; filename="ctrees-%s-%s-%s-%.4f_%.4f_%.4f_%.4f.tif"`, v, years, w.Level.Name, b.West, b.South, b.East, b.North)
			return &cfworker.Response{Status: 200, Header: header, Body: tif}, nil
		}
		meta, _ := json.Marshal(map[string]any{
			"snapshot": d.Snapshot, "variable": v, "year": year, "from": raster.From, "units": units,
			"level": w.Level.Name, "cell_degrees": w.Level.CellDegrees(),
			"rows": w.Rows, "cols": w.Cols, "bbox": [4]float64{b.West, b.South, b.East, b.North},
			"decoding": raster.Decoding, "summary": summary,
			"ms": ms(),
		})
		header["content-type"] = "application/octet-stream"
		header["x-region"] = string(meta)
		header["server-timing"] = fmt.Sprintf("read;dur=%.1f", ms())
		return &cfworker.Response{Status: 200, Header: header, Body: raster.Raw}, nil

	case "/api/series":
		bbox, err := biomass.ParseBBox(q.Get("bbox"))
		if err != nil {
			return nil, httpError{400, err.Error()}
		}
		w, err := biomass.ForSeries(d.Levels, bbox, 16)
		if err != nil {
			return nil, err
		}
		series, err := d.Series(ctx, w)
		if err != nil {
			return nil, err
		}
		header["server-timing"] = fmt.Sprintf("read;dur=%.1f", ms())
		return jsonResponse(header, map[string]any{"snapshot": d.Snapshot, "series": series, "ms": ms()})
	}
	return nil, httpError{404, "not found"}
}

func refs(ctx context.Context, list func(context.Context) ([]string, error), lookup func(context.Context, string) (icechunk.SnapshotID, error)) (map[string]string, error) {
	names, err := list(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, n := range names {
		id, err := lookup(ctx, n)
		if err != nil {
			return nil, err
		}
		out[n] = id.String()
	}
	return out, nil
}

// maxCells bounds the longer side of a region read. At full resolution a
// box of up to 1024 cells touches at most four 2000×2000 chunks.
func maxCells(env cfworker.Env) int {
	if n, err := strconv.Atoi(env.Var("MAX_CELLS")); err == nil && n > 0 {
		return n
	}
	return 1024
}

func jsonResponse(header map[string]string, v any) (*cfworker.Response, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	header["content-type"] = "application/json"
	return &cfworker.Response{Status: 200, Header: header, Body: body}, nil
}
