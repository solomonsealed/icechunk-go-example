// Package biomass reads windows of CTrees' 100 m aboveground biomass from
// its Icechunk repository with icechunk-go and geozarr-go.
//
// A snapshot holds one or more pyramid levels of the same grid: on the
// ard-overviews branch the group's multiscales attribute lists the full
// resolution level and its 5x, 25x, 125x and 625x means; elsewhere the only
// level is the full resolution one. geozarr-go opens the group as xarray
// would, opens the levels of its multiscales layout, and finds each level's
// grid as rioxarray would. A bounding box picks the finest level that keeps
// the read small, selects its cells as rioxarray's clip_box does, and the
// values come with the CF decoding xarray would apply to them.
package biomass

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	geozarr "github.com/solomonsealed/geozarr-go"
	"github.com/solomonsealed/geozarr-go/icechunksrc"
	icechunk "github.com/solomonsealed/icechunk-go"
	zarr "github.com/solomonsealed/zarr-go"
)

// Group is the Zarr group holding the dataset in CTrees' repository.
const Group = "aboveground_biomass"

// Variables are the arrays a window can be read from.
var Variables = []string{"agb", "uncertainty"}

// Level is one resolution of the grid. Transform is the grid's affine
// transform [a, b, c, d, e, f] as rioxarray finds it: a cell's west edge is
// c + a*col and its north edge f + e*row (b and d are 0 on this grid).
type Level struct {
	Name      string         `json:"name"`
	Group     string         `json:"group"`
	Rows      int            `json:"rows"`
	Cols      int            `json:"cols"`
	Transform geozarr.Affine `json:"transform"`

	ds   *geozarr.Dataset
	grid *geozarr.Grid
}

// CellDegrees is the width of a cell in degrees.
func (l *Level) CellDegrees() float64 { return math.Abs(l.Transform[0]) }

// BBox is a longitude/latitude box in degrees.
type BBox struct{ West, South, East, North float64 }

// ParseBBox parses "west,south,east,north".
func ParseBBox(s string) (BBox, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return BBox{}, fmt.Errorf("bbox: want west,south,east,north, got %q", s)
	}
	var v [4]float64
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return BBox{}, fmt.Errorf("bbox: %q is not a number", p)
		}
		v[i] = f
	}
	b := BBox{West: max(v[0], -180), South: max(v[1], -90), East: min(v[2], 180), North: min(v[3], 90)}
	if b.West >= b.East || b.South >= b.North {
		return BBox{}, fmt.Errorf("bbox: %q is empty", s)
	}
	return b, nil
}

// Window is a block of cells of one level.
type Window struct {
	Level      *Level
	Row, Col   int
	Rows, Cols int
}

// Window returns the cells of l that b touches, as rioxarray's clip_box
// selects them: snapped outward to whole cells and clamped to the grid.
// Unlike clip_box, a window may be a single cell wide.
func (l *Level) Window(b BBox) (Window, error) {
	// A tolerance of 1e-4 cells (a centimetre at 1x) keeps float noise in
	// the transform from adding a whole row or column to a box drawn on
	// cell edges, as it would with clip_box alone: CTrees' 1x cell width
	// is 0.00088888888888792, not 1/1125, which is 2e-7 cells off at
	// column 204750. It never takes more than a quarter of the box.
	const eps = 1e-4
	dx := min(eps*math.Abs(l.Transform[0]), (b.East-b.West)/4)
	dy := min(eps*math.Abs(l.Transform[4]), (b.North-b.South)/4)
	w, err := l.grid.ClipBox(b.West+dx, b.South+dy, b.East-dx, b.North-dy, &geozarr.ClipOptions{AllowOneDimensional: true})
	if err != nil {
		return Window{}, fmt.Errorf("bbox on the %s grid: %w", l.Name, err)
	}
	return Window{Level: l, Row: w.RowStart, Col: w.ColStart, Rows: w.RowStop - w.RowStart, Cols: w.ColStop - w.ColStart}, nil
}

// Size is the longer side of w, in cells.
func (w Window) Size() int { return max(w.Rows, w.Cols) }

// BBox returns the edges of w's cells.
func (w Window) BBox() BBox {
	t := w.Level.Transform
	west, north := t.Apply(float64(w.Col), float64(w.Row))
	east, south := t.Apply(float64(w.Col+w.Cols), float64(w.Row+w.Rows))
	return BBox{West: west, South: south, East: east, North: north}
}

// TooLargeError reports a box too large for every level available.
type TooLargeError struct {
	Cells, Max int
	Level      string
}

func (e *TooLargeError) Error() string {
	return fmt.Sprintf("that area is %d cells across at %s, more than the %d this reads at once; select a smaller area or use a branch with overviews", e.Cells, e.Level, e.Max)
}

// ForImage returns the window of the finest level (levels run fine to
// coarse) whose longer side is at most maxCells.
func ForImage(levels []Level, b BBox, maxCells int) (Window, error) {
	var last Window
	for i := range levels {
		w, err := levels[i].Window(b)
		if err != nil {
			return Window{}, err
		}
		if w.Size() <= maxCells {
			return w, nil
		}
		last = w
	}
	return Window{}, &TooLargeError{Cells: last.Size(), Max: maxCells, Level: last.Level.Name}
}

// ErrNoOverviews is returned by ForSeries for a snapshot without overviews:
// 26 years at full resolution is too much to read for a sketch.
var ErrNoOverviews = errors.New("this ref has no overviews, so there is no cheap way to read all 26 years; use ard-overviews")

// ForSeries returns the window of the coarsest overview level whose longer
// side is at least minCells, or the finest overview when the box is smaller
// than that everywhere. Overview cells are means of the cells below them, so
// a coarse window's mean is close to the full-resolution mean at a fraction
// of the reads.
func ForSeries(levels []Level, b BBox, minCells int) (Window, error) {
	if len(levels) < 2 {
		return Window{}, ErrNoOverviews
	}
	overviews := levels[1:]
	for i := len(overviews) - 1; i >= 0; i-- {
		w, err := overviews[i].Window(b)
		if err != nil {
			return Window{}, err
		}
		if w.Size() >= minCells || i == 0 {
			return w, nil
		}
	}
	panic("unreachable")
}

// Dataset is one snapshot of the repository. Attrs are each variable's
// attributes as xarray shows them: what CF decoding uses (scale_factor,
// _FillValue, ...) is in Decoding instead.
type Dataset struct {
	Snapshot string                    `json:"snapshot"`
	Levels   []Level                   `json:"levels"`
	Years    []int                     `json:"years"`
	Attrs    map[string]map[string]any `json:"attributes"`
}

// Open reads the levels, years and variable attributes of a snapshot.
func Open(ctx context.Context, s *icechunk.Session) (*Dataset, error) {
	d := &Dataset{Snapshot: s.SnapshotID().String(), Attrs: map[string]map[string]any{}}
	root, err := icechunksrc.Open(ctx, s, Group)
	if err != nil {
		return nil, err
	}
	ms, err := root.Multiscales()
	if err != nil {
		return nil, err
	}
	groups, names := []*geozarr.Dataset{root}, []string{"1x"}
	if ms != nil {
		groups, names = nil, nil
		for _, l := range ms.Layout {
			g, err := root.OpenLevel(ctx, l)
			if err != nil {
				return nil, err
			}
			name := l.Asset
			if name == "." {
				name = "1x"
			}
			groups, names = append(groups, g), append(names, name)
		}
	}
	// Finding a grid reads the ends of its x and y coordinates: all levels
	// at once.
	d.Levels = make([]Level, len(groups))
	errs := make([]error, len(groups))
	var wg sync.WaitGroup
	for i := range groups {
		wg.Go(func() { d.Levels[i], errs[i] = level(ctx, names[i], groups[i]) })
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	sort.SliceStable(d.Levels, func(i, j int) bool { return d.Levels[i].CellDegrees() < d.Levels[j].CellDegrees() })

	tv := root.Variable("time")
	if tv == nil {
		return nil, fmt.Errorf("biomass: %s has no time coordinate", Group)
	}
	times, err := tv.Read(ctx)
	if err != nil {
		return nil, err
	}
	// Dates are times in a calendar other than the standard ones.
	for _, t := range times.Dates {
		d.Years = append(d.Years, t.Year)
	}
	if times.Values != nil {
		ts, err := times.Values.Times()
		if err != nil {
			return nil, err
		}
		for _, t := range ts {
			d.Years = append(d.Years, t.Year())
		}
	}
	for _, v := range Variables {
		d.Attrs[v] = d.Levels[0].ds.Variable(v).Attributes
	}
	return d, nil
}

// level returns the level of the grid of ds's agb, which must be on
// longitude/latitude, with the variables of every year stored as int16 on
// it.
func level(ctx context.Context, name string, ds *geozarr.Dataset) (Level, error) {
	g, err := ds.Grid(ctx, "agb")
	if err != nil {
		return Level{}, fmt.Errorf("biomass: level %s: %w", name, err)
	}
	crs, err := g.CRS()
	if err != nil {
		return Level{}, fmt.Errorf("biomass: level %s: %w", name, err)
	}
	if crs == nil || crs.Code != "EPSG:4326" {
		return Level{}, fmt.Errorf("biomass: level %s is not on EPSG:4326 (%v)", name, crs)
	}
	t, err := g.Transform(true)
	if err != nil {
		return Level{}, fmt.Errorf("biomass: level %s: %w", name, err)
	}
	for _, v := range Variables {
		a := ds.Variable(v)
		if a == nil || !slices.Equal(a.Dims, []string{"time", g.YDim, g.XDim}) || a.Array.DataType().Name != "int16" {
			return Level{}, fmt.Errorf("biomass: level %s has no int16 %s over time, %s and %s", name, v, g.YDim, g.XDim)
		}
	}
	return Level{Name: name, Group: ds.Path, Rows: g.Height, Cols: g.Width, Transform: t, ds: ds, grid: g}, nil
}

// Decoding is how a variable's stored integers become values: CF's
// value = stored*scale_factor + add_offset, with Fill marking no data.
type Decoding struct {
	Scale  float64 `json:"scale_factor"`
	Offset float64 `json:"add_offset"`
	Fill   int16   `json:"fill_value"`
}

// Decoding returns the CF decoding of variable v at full resolution.
func (d *Dataset) Decoding(v string) Decoding {
	return decoding(d.Levels[0].ds.Variable(v))
}

// decoding returns the CF decoding xarray would apply to v (its encoding).
// A missing scale_factor is 1, as in CF.
func decoding(v *geozarr.Variable) Decoding {
	dec := Decoding{Scale: 1, Fill: -9999}
	if f, ok := number(v.Encoding["scale_factor"]); ok {
		dec.Scale = f
	}
	if f, ok := number(v.Encoding["add_offset"]); ok {
		dec.Offset = f
	}
	if f, ok := number(v.Encoding["_FillValue"]); ok {
		dec.Fill = int16(f)
	}
	return dec
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int64:
		return float64(n), true
	}
	return 0, false
}

// Raster is one year of one variable over a window, north-up, row-major;
// or, when From is set, the change from year From to Year.
type Raster struct {
	Window   Window
	Variable string
	Year     int
	From     int
	Values   []int16
	Raw      []byte // Values as little-endian bytes
	Decoding Decoding
}

// Title names what r holds, e.g. "agb 2025" or "agb change 2000–2025".
func (r *Raster) Title() string {
	if r.From != 0 {
		return fmt.Sprintf("%s change %d–%d", r.Variable, r.From, r.Year)
	}
	return fmt.Sprintf("%s %d", r.Variable, r.Year)
}

func (d *Dataset) yearIndex(year int) (int, error) {
	for i, y := range d.Years {
		if y == year {
			return i, nil
		}
	}
	return 0, fmt.Errorf("no year %d (the data covers %d–%d)", year, d.Years[0], d.Years[len(d.Years)-1])
}

func checkVariable(v string) error {
	for _, x := range Variables {
		if x == v {
			return nil
		}
	}
	return fmt.Errorf("unknown variable %q (want agb or uncertainty)", v)
}

// Read reads one year of variable v over w.
func (d *Dataset) Read(ctx context.Context, w Window, v string, year int) (*Raster, error) {
	if err := checkVariable(v); err != nil {
		return nil, err
	}
	t, err := d.yearIndex(year)
	if err != nil {
		return nil, err
	}
	// The stored integers, not the floats geozarr would decode them to:
	// they are a quarter of the size, and the page decodes them itself.
	a := w.Level.ds.Variable(v)
	nd, err := a.Array.Read(ctx, []uint64{uint64(t), uint64(w.Row), uint64(w.Col)}, []uint64{1, uint64(w.Rows), uint64(w.Cols)})
	if err != nil {
		return nil, err
	}
	vals, err := zarr.Convert[int16](nd)
	if err != nil {
		return nil, err
	}
	return &Raster{Window: w, Variable: v, Year: year, Values: vals, Raw: nd.Data, Decoding: decoding(a)}, nil
}

// Series is the mean of agb and of its uncertainty over a window, per year.
type Series struct {
	Level       string     `json:"level"`
	BBox        [4]float64 `json:"bbox"`
	Rows        int        `json:"rows"`
	Cols        int        `json:"cols"`
	Years       []int      `json:"years"`
	AGB         []Num      `json:"agb"`
	Uncertainty []Num      `json:"uncertainty"`
	Coverage    []Num      `json:"coverage"`
}

// Series reads every year of agb and uncertainty over w and averages them.
func (d *Dataset) Series(ctx context.Context, w Window) (*Series, error) {
	n := len(d.Years)
	read := func(v string) ([]int16, error) {
		a := w.Level.ds.Variable(v).Array
		nd, err := a.Read(ctx, []uint64{0, uint64(w.Row), uint64(w.Col)}, []uint64{uint64(n), uint64(w.Rows), uint64(w.Cols)})
		if err != nil {
			return nil, err
		}
		return zarr.Convert[int16](nd)
	}
	// Both arrays at once: chunk fetches are the cost, and they overlap.
	var unc []int16
	var uncErr error
	done := make(chan struct{})
	go func() { defer close(done); unc, uncErr = read("uncertainty") }()
	agb, err := read("agb")
	<-done
	if err != nil {
		return nil, err
	}
	if uncErr != nil {
		return nil, uncErr
	}
	b := w.BBox()
	s := &Series{Level: w.Level.Name, BBox: [4]float64{b.West, b.South, b.East, b.North}, Rows: w.Rows, Cols: w.Cols, Years: d.Years}
	per := w.Rows * w.Cols
	da, du := decoding(w.Level.ds.Variable("agb")), decoding(w.Level.ds.Variable("uncertainty"))
	for t := range n {
		sa := Summarize(agb[t*per:(t+1)*per], w, da)
		su := Summarize(unc[t*per:(t+1)*per], w, du)
		s.AGB = append(s.AGB, Num(sa.Mean))
		s.Uncertainty = append(s.Uncertainty, Num(su.Mean))
		s.Coverage = append(s.Coverage, Num(float64(sa.Valid)/float64(per)))
	}
	return s, nil
}

// Num is a float64 that encodes NaN as JSON null.
type Num float64

func (n Num) MarshalJSON() ([]byte, error) {
	if math.IsNaN(float64(n)) || math.IsInf(float64(n), 0) {
		return []byte("null"), nil
	}
	return strconv.AppendFloat(nil, float64(n), 'g', 6, 64), nil
}
