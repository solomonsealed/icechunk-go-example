package biomass

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"strings"
	"testing"

	geozarr "github.com/solomonsealed/geozarr-go"
)

func TestOpen(t *testing.T) {
	d := testDataset(t)
	// The levels of the multiscales layout, fine to coarse.
	want := []Level{
		{Name: "1x", Group: Group, Rows: 202500, Cols: 405000, Transform: geozarr.Affine{0.0008888888888805013, 0, -180, 0, -0.0008888888888947122, 90}},
		{Name: "5x", Group: Group + "/5x", Rows: 40500, Cols: 81000, Transform: geozarr.Affine{0.004444444444430928, 0, -180, 0, -0.004444444444445139, 90}},
		{Name: "25x", Group: Group + "/25x", Rows: 8100, Cols: 16200, Transform: geozarr.Affine{0.022222222222183063, 0, -180, 0, -0.022222222222183063, 89.99999999999997}},
		{Name: "125x", Group: Group + "/125x", Rows: 1620, Cols: 3240, Transform: geozarr.Affine{0.11111111111097216, 0, -179.99999999999997, 0, -0.11111111111098637, 90}},
		{Name: "625x", Group: Group + "/625x", Rows: 324, Cols: 648, Transform: geozarr.Affine{0.5555555555549461, 0, -180, 0, -0.5555555555549603, 90}},
	}
	if len(d.Levels) != len(want) {
		t.Fatalf("%d levels", len(d.Levels))
	}
	for i, l := range d.Levels {
		w := want[i]
		if l.Name != w.Name || l.Group != w.Group || l.Rows != w.Rows || l.Cols != w.Cols || l.Transform != w.Transform {
			t.Errorf("level %d: %+v, want %+v", i, l, w)
		}
	}
	if len(d.Years) != 26 || d.Years[0] != 2000 || d.Years[25] != 2025 {
		t.Errorf("years %v", d.Years)
	}
	if dec := d.Decoding("agb"); dec != (Decoding{Scale: 0.1, Fill: -9999}) {
		t.Errorf("decoding %+v", dec)
	}
	// As in xarray, what CF decoding uses leaves the attributes.
	a := d.Attrs["agb"]
	_, scale := a["scale_factor"]
	_, agbScale := a["agb_scale_factor"]
	if a["units"] != "Mg ha-1" || scale || !agbScale {
		t.Errorf("attributes %v", a)
	}
}

func TestParseBBox(t *testing.T) {
	b, err := ParseBBox("-123.3, 49.2,-123.0,49.4")
	if err != nil || b != (BBox{-123.3, 49.2, -123.0, 49.4}) {
		t.Fatalf("got %v, %v", b, err)
	}
	if b, _ := ParseBBox("-200,-95,200,95"); b != (BBox{-180, -90, 180, 90}) {
		t.Errorf("not clamped: %v", b)
	}
	for _, bad := range []string{"", "1,2,3", "1,2,3,x", "5,0,4,1", "0,1,1,1", "NaN,0,1,1"} {
		if _, err := ParseBBox(bad); err == nil {
			t.Errorf("ParseBBox(%q) accepted", bad)
		}
	}
}

func TestWindowSnapsToCells(t *testing.T) {
	levels := testDataset(t).Levels
	l := &levels[0]
	// 1/1125 degree cells: 0.1 degree is 112.5 cells, so both edges round out.
	w, err := l.Window(BBox{West: 0.05, South: -0.1, East: 0.15, North: 0})
	if err != nil {
		t.Fatal(err)
	}
	if w.Col != 202556 || w.Cols != 113 || w.Row != 101250 || w.Rows != 113 {
		t.Errorf("window %+v", w)
	}
	got := w.BBox()
	const tol = 1e-9 // the transform's own float noise
	if got.West > 0.05+tol || got.East < 0.15-tol || got.North < -tol || got.South > -0.1+tol {
		t.Errorf("snapped box %v does not contain the request", got)
	}
	// A box exactly on cell edges takes exactly those cells, though the
	// noise puts some of its edges just inside the next cell.
	w, _ = l.Window(BBox{West: 1, South: -1, East: 2, North: 0})
	if w.Cols != 1125 || w.Rows != 1125 {
		t.Errorf("1 degree is %dx%d cells, want 1125x1125", w.Rows, w.Cols)
	}
	// Clamped to the grid.
	w, _ = levels[4].Window(BBox{-180, -90, 180, 90})
	if w.Row != 0 || w.Col != 0 || w.Rows != 324 || w.Cols != 648 {
		t.Errorf("world window %+v", w)
	}
	// A box within one cell is that cell.
	w, err = levels[4].Window(BBox{0.1, 0.1, 0.2, 0.2})
	if err != nil || w.Rows != 1 || w.Cols != 1 {
		t.Errorf("window in one cell %+v, %v", w, err)
	}
}

func TestForImage(t *testing.T) {
	testLevels := testDataset(t).Levels
	cases := []struct {
		box  BBox
		want string
	}{
		{BBox{-123.2, 49.2, -123.0, 49.3}, "1x"}, // 225 cells
		{BBox{-124, 49, -123, 50}, "5x"},         // 1125 at 1x, 225 at 5x
		{BBox{-130, 40, -110, 50}, "25x"},        // 4500 at 5x, 900 at 25x
		{BBox{-180, -90, 180, 90}, "625x"},       // 3240 at 125x
	}
	for _, c := range cases {
		w, err := ForImage(testLevels, c.box, 1024)
		if err != nil || w.Level.Name != c.want {
			t.Errorf("%v: got %v (%v), want %s", c.box, w.Level, err, c.want)
		}
	}
	var tooLarge *TooLargeError
	if _, err := ForImage(testLevels[:1], BBox{-124, 49, -123, 50}, 1024); !errors.As(err, &tooLarge) || tooLarge.Cells != 1125 || tooLarge.Level != "1x" {
		t.Errorf("full resolution only: got %v", err)
	}
}

func TestForSeries(t *testing.T) {
	testLevels := testDataset(t).Levels
	if _, err := ForSeries(testLevels[:1], BBox{0, 0, 1, 1}, 16); !errors.Is(err, ErrNoOverviews) {
		t.Errorf("no overviews: %v", err)
	}
	cases := []struct {
		box  BBox
		want string
	}{
		{BBox{0, 0, 0.01, 0.01}, "5x"},     // smaller than 16 cells everywhere
		{BBox{0, 0, 0.5, 0.5}, "25x"},      // 23 cells at 25x
		{BBox{0, 0, 3, 3}, "125x"},         // 27 cells at 125x
		{BBox{-180, -90, 180, 90}, "625x"}, // the world
	}
	for _, c := range cases {
		w, err := ForSeries(testLevels, c.box, 16)
		if err != nil || w.Level.Name != c.want {
			t.Errorf("%v: got %v (%v), want %s", c.box, w.Level, err, c.want)
		}
	}
}

func TestCellAreasCoverTheEarth(t *testing.T) {
	testLevels := testDataset(t).Levels
	l := &testLevels[4]
	var total float64
	for r := range l.Rows {
		total += l.CellAreaHa(r) * float64(l.Cols)
	}
	want := 4 * math.Pi * earthRadius * earthRadius / 1e4
	if math.Abs(total-want)/want > 1e-9 {
		t.Errorf("cells cover %.6g ha, want %.6g", total, want)
	}
	// About 100 m: a 1x cell at the equator is 0.98 ha.
	if a := testLevels[0].CellAreaHa(101250); math.Abs(a-0.9775) > 0.001 {
		t.Errorf("equatorial cell %.4f ha", a)
	}
}

func TestSummarize(t *testing.T) {
	l := &Level{Name: "t", Rows: 2, Cols: 2, Transform: [6]float64{1, 0, 0, 0, -1, 1}}
	w := Window{Level: l, Rows: 2, Cols: 2}
	dec := Decoding{Scale: 0.1, Fill: -9999}
	s := Summarize([]int16{100, -9999, 300, 500}, w, dec)
	if s.Valid != 3 || s.Cells != 4 {
		t.Fatalf("valid %d of %d", s.Valid, s.Cells)
	}
	top, bottom := l.CellAreaHa(0), l.CellAreaHa(1)
	wantTotal := 10*top + 30*bottom + 50*bottom
	if math.Abs(float64(s.Total)-wantTotal) > 1e-6*wantTotal {
		t.Errorf("total %v, want %v", s.Total, wantTotal)
	}
	if want := wantTotal / (top + 2*bottom); math.Abs(float64(s.Mean)-want) > 1e-9*want {
		t.Errorf("mean %v, want %v", s.Mean, want)
	}
	if s := Summarize([]int16{-9999, -9999, -9999, -9999}, w, dec); !math.IsNaN(float64(s.Mean)) {
		t.Errorf("mean of nothing is %v", s.Mean)
	}
}

func TestGeoTIFF(t *testing.T) {
	l := &testDataset(t).Levels[0]
	w, _ := l.Window(BBox{-123.2, 49.2, -123.19, 49.205})
	vals := make([]int16, w.Rows*w.Cols)
	raw := make([]byte, 2*len(vals))
	for i := range vals {
		vals[i] = int16(i) - 7
		binary.LittleEndian.PutUint16(raw[2*i:], uint16(vals[i]))
	}
	r := &Raster{Window: w, Variable: "agb", Year: 2025, Values: vals, Raw: raw, Decoding: Decoding{Scale: 0.1, Fill: -9999}}
	data, err := GeoTIFF(r, "Mg ha-1", map[string]string{"snapshot": "Q0GD2YB5Y727KYMGCW70", "source": "a < b & c"})
	if err != nil {
		t.Fatal(err)
	}
	if string(data[:4]) != "II*\x00" {
		t.Fatalf("header %q", data[:4])
	}
	le := binary.LittleEndian
	ifd := le.Uint32(data[4:])
	n := int(le.Uint16(data[ifd:]))
	tags := map[uint16][]byte{}
	sizes := map[uint16]uint32{1: 1, 2: 1, 3: 2, 4: 4, 12: 8}
	prev := uint16(0)
	for i := range n {
		e := data[int(ifd)+2+12*i:]
		tag, typ, count := le.Uint16(e), le.Uint16(e[2:]), le.Uint32(e[4:])
		if tag <= prev {
			t.Errorf("tag %d out of order", tag)
		}
		prev = tag
		size := sizes[typ] * count
		if size <= 4 {
			tags[tag] = e[8 : 8+size]
		} else {
			off := le.Uint32(e[8:])
			tags[tag] = data[off : off+size]
		}
	}
	if le.Uint32(tags[256]) != uint32(w.Cols) || le.Uint32(tags[257]) != uint32(w.Rows) {
		t.Errorf("size %dx%d", le.Uint32(tags[256]), le.Uint32(tags[257]))
	}
	off, count := le.Uint32(tags[273]), le.Uint32(tags[279])
	zr, err := zlib.NewReader(bytes.NewReader(data[off : off+count]))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(zr)
	if !bytes.Equal(got, raw) {
		t.Error("strip does not decompress to the raster")
	}
	tie := tags[33922]
	west, north := math.Float64frombits(le.Uint64(tie[24:])), math.Float64frombits(le.Uint64(tie[32:]))
	if b := w.BBox(); west != b.West || north != b.North {
		t.Errorf("tie point %v,%v, want %v,%v", west, north, b.West, b.North)
	}
	if s := string(tags[42113]); s != "-9999\x00" {
		t.Errorf("nodata %q", s)
	}
	md := string(tags[42112])
	for _, want := range []string{`role="scale">0.1<`, `name="source">a &lt; b &amp; c<`, `role="unittype">Mg ha-1<`} {
		if !strings.Contains(md, want) {
			t.Errorf("metadata lacks %s: %s", want, md)
		}
	}
}

func raster(w Window, year int, vals ...int16) *Raster {
	raw := make([]byte, 2*len(vals))
	for i, v := range vals {
		binary.LittleEndian.PutUint16(raw[2*i:], uint16(v))
	}
	return &Raster{Window: w, Variable: "agb", Year: year, Values: vals, Raw: raw, Decoding: Decoding{Scale: 0.1, Fill: -9999}}
}

func TestChange(t *testing.T) {
	l := &Level{Name: "t", Rows: 2, Cols: 3, Transform: [6]float64{1, 0, 0, 0, -1, 1}}
	w := Window{Level: l, Rows: 2, Cols: 3}
	a := raster(w, 2000, 6000, 100, -9999, 50, 0, 300)
	b := raster(w, 2025, 0, 100, 40, -9999, 6000, 250)
	c, err := Change(a, b)
	if err != nil {
		t.Fatal(err)
	}
	want := []int16{-6000, 0, -9999, -9999, 6000, -50}
	for i, v := range want {
		if c.Values[i] != v || int16(binary.LittleEndian.Uint16(c.Raw[2*i:])) != v {
			t.Errorf("cell %d: %d (raw %d), want %d", i, c.Values[i], int16(binary.LittleEndian.Uint16(c.Raw[2*i:])), v)
		}
	}
	if c.From != 2000 || c.Year != 2025 || c.Title() != "agb change 2000–2025" {
		t.Errorf("from %d year %d title %q", c.From, c.Year, c.Title())
	}
	if _, err := Change(a, raster(Window{Level: l, Rows: 1, Cols: 3}, 2025, 1, 2, 3)); err == nil {
		t.Error("rasters of different windows accepted")
	}

	// ±10 t/ha is 100 stored: -6000 and -50 → one loss at 600 t/ha, the
	// -5 t/ha cell is inside the band; +6000 is a gain.
	s := SummarizeChange(c, MinChange)
	top, bottom := l.CellAreaHa(0), l.CellAreaHa(1)
	if s.Valid != 4 || math.Abs(float64(s.LossHa)-top) > 1e-6 || math.Abs(float64(s.GainHa)-bottom) > 1e-6 {
		t.Errorf("summary %+v; top %v bottom %v", s, top, bottom)
	}
	if want := -600*top + 600*bottom - 5*bottom; math.Abs(float64(s.Total)-want) > 1e-6*math.Abs(want) {
		t.Errorf("net %v, want %v", s.Total, want)
	}
}
