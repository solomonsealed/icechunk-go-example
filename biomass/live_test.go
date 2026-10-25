package biomass

import (
	"context"
	"math"
	"os"
	"testing"

	icechunk "github.com/solomonsealed/icechunk-go"
	"github.com/solomonsealed/icechunk-go/storage"
	"github.com/solomonsealed/icechunk-go/storage/httpstore"
)

// The live tests read CTrees' public repository on S3:
//
//	CTREES_LIVE=1 go test ./biomass -run Live -v
func openLive(t *testing.T, ref string) *Dataset {
	t.Helper()
	if os.Getenv("CTREES_LIVE") == "" {
		t.Skip("set CTREES_LIVE=1 to read the CTrees repository on S3")
	}
	ctx := context.Background()
	st := httpstore.NewS3(storage.S3Config{Bucket: "ctrees-agb-100m-global", Prefix: "agb_100m_global", Region: "us-west-2", Anonymous: true}, nil)
	repo, err := icechunk.Open(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := repo.ReadonlySession(ctx, icechunk.AtBranch(ref))
	if err != nil {
		t.Fatal(err)
	}
	d, err := Open(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestLiveLevels(t *testing.T) {
	ao := openLive(t, "ard-overviews")
	if len(ao.Levels) != 5 || ao.Levels[0].Name != "1x" || ao.Levels[4].Name != "625x" {
		t.Fatalf("levels %+v", ao.Levels)
	}
	if ao.Years[0] != 2000 || ao.Years[len(ao.Years)-1] != 2025 {
		t.Errorf("years %v", ao.Years)
	}
	if dec := ao.Decoding("agb"); dec.Scale != 0.1 || dec.Fill != -9999 {
		t.Errorf("ard-overviews decoding %+v", dec)
	}
	// Without multiscales the only level is the group's own grid; it must
	// be the full resolution level of the layout.
	ard := openLive(t, "ard")
	if len(ard.Levels) != 1 {
		t.Fatalf("ard levels %+v", ard.Levels)
	}
	a, b := ard.Levels[0], ao.Levels[0]
	if a.Rows != b.Rows || a.Cols != b.Cols {
		t.Errorf("shape %dx%d, want %dx%d", a.Rows, a.Cols, b.Rows, b.Cols)
	}
	for i, v := range a.Transform {
		if math.Abs(v-b.Transform[i]) > 1e-9 {
			t.Errorf("transform[%d] = %v from coordinates, %v from multiscales", i, v, b.Transform[i])
		}
	}
	// main is the same data with the CF scale_factor left at 1.
	if dec := openLive(t, "main").Decoding("agb"); dec.Scale != 1 {
		t.Errorf("main scale_factor %v", dec.Scale)
	}
}

// A 5x cell is the mean of the 5x5 full-resolution cells below it, so the
// two levels' windows must line up.
func TestLiveOverviewIsMeanOfCells(t *testing.T) {
	d := openLive(t, "ard-overviews")
	ctx := context.Background()
	box := BBox{West: -60.2, South: -5.2, East: -60.1, North: -5.1} // Amazonas, Brazil
	w5, err := d.Levels[1].Window(box)
	if err != nil {
		t.Fatal(err)
	}
	coarse, err := d.Read(ctx, w5, "agb", 2025)
	if err != nil {
		t.Fatal(err)
	}
	full := Window{Level: &d.Levels[0], Row: w5.Row * 5, Col: w5.Col * 5, Rows: w5.Rows * 5, Cols: w5.Cols * 5}
	if a, b := full.BBox(), w5.BBox(); math.Abs(a.West-b.West)+math.Abs(a.East-b.East)+math.Abs(a.North-b.North)+math.Abs(a.South-b.South) > 1e-8 {
		t.Fatalf("5x window %v covers %v at 1x", w5.BBox(), full.BBox())
	}
	fine, err := d.Read(ctx, full, "agb", 2025)
	if err != nil {
		t.Fatal(err)
	}
	var worst float64
	for r := range w5.Rows {
		for c := range w5.Cols {
			var sum float64
			var n int
			for i := range 5 {
				for j := range 5 {
					if v := fine.Values[(5*r+i)*full.Cols+5*c+j]; v != -9999 {
						sum += float64(v)
						n++
					}
				}
			}
			got := coarse.Values[r*w5.Cols+c]
			if n == 0 {
				continue
			}
			worst = max(worst, math.Abs(float64(got)-sum/float64(n)))
		}
	}
	// int16 means are rounded or truncated.
	if worst > 1 {
		t.Errorf("a 5x cell is %v (×0.1 Mg/ha) from the mean of its cells", worst)
	}
	s := Summarize(fine.Values, full, fine.Decoding)
	t.Logf("%v: %dx%d cells at 1x, mean %.1f Mg/ha, total %.0f Mg; 5x mean %.1f", w5.BBox(), full.Rows, full.Cols,
		s.Mean, s.Total, Summarize(coarse.Values, w5, coarse.Decoding).Mean)
}

func TestLiveSeries(t *testing.T) {
	d := openLive(t, "ard-overviews")
	w, err := ForSeries(d.Levels, BBox{West: -121.7, South: 39.7, East: -121.5, North: 39.85}, 16) // Paradise, CA
	if err != nil {
		t.Fatal(err)
	}
	s, err := d.Series(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.AGB) != 26 {
		t.Fatalf("%d years", len(s.AGB))
	}
	// The Camp Fire burned Paradise in November 2018.
	if !(s.AGB[19] < s.AGB[17]) {
		t.Errorf("no loss after the Camp Fire: 2017 %v, 2019 %v", s.AGB[17], s.AGB[19])
	}
	t.Logf("%s %dx%d: 2000 %.1f, 2017 %.1f, 2019 %.1f, 2025 %.1f Mg/ha", s.Level, s.Rows, s.Cols, s.AGB[0], s.AGB[17], s.AGB[19], s.AGB[25])
}

// Paradise, California burned in November 2018: from 2017 to 2019 the town
// lost far more biomass than it gained.
func TestLiveChange(t *testing.T) {
	d := openLive(t, "ard-overviews")
	w, err := ForImage(d.Levels, BBox{West: -121.7, South: 39.7, East: -121.5, North: 39.85}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	c, err := d.ReadChange(context.Background(), w, "agb", 2017, 2019)
	if err != nil {
		t.Fatal(err)
	}
	s := SummarizeChange(c, MinChange)
	if !(s.Mean < 0 && s.LossHa > 5*s.GainHa) {
		t.Errorf("no clear loss: %+v", s)
	}
	t.Logf("%s %dx%d 2017→2019: mean %.1f t/ha, net %.0f t, loss %.0f ha, gain %.0f ha of %.0f ha",
		w.Level.Name, w.Rows, w.Cols, s.Mean, s.Total, s.LossHa, s.GainHa, s.AreaHa)
}
