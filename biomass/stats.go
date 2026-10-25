package biomass

import "math"

// earthRadius is the WGS84 authalic radius in metres: a sphere of the
// ellipsoid's area, so cell areas sum to the right total.
const earthRadius = 6371007.181

// Summary describes the valid cells of a raster. Cells on a
// longitude/latitude grid shrink towards the poles, so the mean is weighted
// by each cell's area and the total is value × area.
type Summary struct {
	Mean   Num `json:"mean"`    // area-weighted mean, decoded units (Mg/ha)
	Total  Num `json:"total"`   // Σ value × area: Mg for agb
	AreaHa Num `json:"area_ha"` // area of the valid cells
	Valid  int `json:"valid"`
	Cells  int `json:"cells"`
}

// CellAreaHa is the area in hectares of a cell of l in row r.
func (l *Level) CellAreaHa(r int) float64 {
	t := l.Transform
	north := (t[5] + t[4]*float64(r)) * math.Pi / 180
	south := (t[5] + t[4]*float64(r+1)) * math.Pi / 180
	width := math.Abs(t[0]) * math.Pi / 180
	return earthRadius * earthRadius * width * math.Abs(math.Sin(north)-math.Sin(south)) / 1e4
}

// Summarize decodes vals (a raster over w) and summarises its valid cells.
func Summarize(vals []int16, w Window, dec Decoding) Summary {
	s := Summary{Cells: len(vals)}
	var sum, area float64
	for i := range w.Rows {
		cell := w.Level.CellAreaHa(w.Row + i)
		for _, v := range vals[i*w.Cols : (i+1)*w.Cols] {
			if v == dec.Fill {
				continue
			}
			s.Valid++
			sum += (float64(v)*dec.Scale + dec.Offset) * cell
			area += cell
		}
	}
	s.Mean, s.Total, s.AreaHa = Num(math.NaN()), Num(sum), Num(area)
	if area > 0 {
		s.Mean = Num(sum / area)
	}
	return s
}
