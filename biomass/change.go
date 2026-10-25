package biomass

import (
	"context"
	"fmt"
)

// MinChange is the change, in decoded units (t/ha for agb), below which a
// cell counts as unchanged: the model's year-to-year noise in undisturbed
// forest is a few t/ha (zarro/plans/biomass/PLAN.md §4.2), and the World
// Biomass Map leaves the same ±10 t/ha band clear.
const MinChange = 10

// ReadChange reads variable v over w in two years and returns to − from,
// in stored units, as a raster whose From is set. A cell is fill unless it
// is valid in both years.
//
// The two years are read one after the other: at full resolution each read
// may hold four 2000×2000 chunks at once, and two at a time would not fit
// in a Worker's 128 MB.
func (d *Dataset) ReadChange(ctx context.Context, w Window, v string, from, to int) (*Raster, error) {
	a, err := d.Read(ctx, w, v, from)
	if err != nil {
		return nil, err
	}
	b, err := d.Read(ctx, w, v, to)
	if err != nil {
		return nil, err
	}
	return Change(a, b)
}

// Change returns b − a for two rasters of the same window and variable.
// Stored values lie in valid_min…valid_max (0…6000), so a difference always
// fits in int16 and never collides with the fill value.
func Change(a, b *Raster) (*Raster, error) {
	if a.Window != b.Window || a.Variable != b.Variable || a.Decoding != b.Decoding {
		return nil, fmt.Errorf("change: rasters differ in window, variable or decoding")
	}
	fill := a.Decoding.Fill
	out := &Raster{Window: a.Window, Variable: a.Variable, From: a.Year, Year: b.Year, Decoding: a.Decoding,
		Values: make([]int16, len(a.Values)), Raw: make([]byte, 2*len(a.Values))}
	for i, x := range a.Values {
		y := b.Values[i]
		d := fill
		if x != fill && y != fill {
			d = y - x
		}
		out.Values[i] = d
		out.Raw[2*i], out.Raw[2*i+1] = byte(d), byte(uint16(d)>>8)
	}
	// A change has no offset: add_offset cancels out.
	out.Decoding.Offset = 0
	return out, nil
}

// ChangeSummary describes a change raster: Mean, Total and AreaHa are the
// area-weighted mean change, the net change (Mg for agb) and the area valid
// in both years; LossHa and GainHa are the areas that moved by at least
// Band, down and up.
type ChangeSummary struct {
	Summary
	Band   float64 `json:"band"`
	LossHa Num     `json:"loss_ha"`
	GainHa Num     `json:"gain_ha"`
}

// SummarizeChange summarises a raster returned by Change.
func SummarizeChange(r *Raster, band float64) ChangeSummary {
	s := ChangeSummary{Summary: Summarize(r.Values, r.Window, r.Decoding), Band: band}
	w, dec := r.Window, r.Decoding
	var loss, gain float64
	for i := range w.Rows {
		cell := w.Level.CellAreaHa(w.Row + i)
		for _, v := range r.Values[i*w.Cols : (i+1)*w.Cols] {
			if v == dec.Fill {
				continue
			}
			switch c := float64(v) * dec.Scale; {
			case c <= -band:
				loss += cell
			case c >= band:
				gain += cell
			}
		}
	}
	s.LossHa, s.GainHa = Num(loss), Num(gain)
	return s
}
