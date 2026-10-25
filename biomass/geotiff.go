package biomass

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"html"
	"math"
	"sort"
	"strconv"
)

// GeoTIFF encodes r as a single-band int16 GeoTIFF on EPSG:4326: one
// deflate-compressed strip, the fill value as GDAL's nodata, and the CF
// scale and offset in GDAL's metadata so that QGIS and rasterio show Mg/ha.
// items are written as dataset metadata (source, snapshot, ...).
func GeoTIFF(r *Raster, units string, items map[string]string) ([]byte, error) {
	w := r.Window
	var strip bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&strip, zlib.DefaultCompression)
	if _, err := zw.Write(r.Raw); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}

	var md bytes.Buffer
	md.WriteString("<GDALMetadata>")
	keys := make([]string, 0, len(items))
	for k := range items {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&md, `<Item name="%s">%s</Item>`, html.EscapeString(k), html.EscapeString(items[k]))
	}
	fmt.Fprintf(&md, `<Item name="SCALE" sample="0" role="scale">%s</Item>`, strconv.FormatFloat(r.Decoding.Scale, 'g', -1, 64))
	fmt.Fprintf(&md, `<Item name="OFFSET" sample="0" role="offset">%s</Item>`, strconv.FormatFloat(r.Decoding.Offset, 'g', -1, 64))
	fmt.Fprintf(&md, `<Item name="UNITTYPE" sample="0" role="unittype">%s</Item>`, html.EscapeString(units))
	fmt.Fprintf(&md, `<Item name="DESCRIPTION" sample="0" role="description">%s</Item>`, html.EscapeString(r.Title()))
	md.WriteString("</GDALMetadata>")

	b := w.BBox()
	t := w.Level.Transform
	const (
		short  = 3
		long   = 4
		ascii  = 2
		double = 12
	)
	type entry struct {
		tag, typ uint16
		count    uint32
		data     []byte // little-endian values
	}
	shorts := func(v ...uint16) []byte {
		out := make([]byte, 2*len(v))
		for i, x := range v {
			binary.LittleEndian.PutUint16(out[2*i:], x)
		}
		return out
	}
	longs := func(v ...uint32) []byte {
		out := make([]byte, 4*len(v))
		for i, x := range v {
			binary.LittleEndian.PutUint32(out[4*i:], x)
		}
		return out
	}
	doubles := func(v ...float64) []byte {
		out := make([]byte, 8*len(v))
		for i, x := range v {
			binary.LittleEndian.PutUint64(out[8*i:], math.Float64bits(x))
		}
		return out
	}
	str := func(s string) []byte { return append([]byte(s), 0) }

	// The strip follows the header, then the IFD and its out-of-line values.
	const header = 8
	stripOffset := uint32(header)
	ifdOffset := stripOffset + uint32(strip.Len())
	ifdOffset += ifdOffset & 1 // word-aligned
	nodata := strconv.Itoa(int(r.Decoding.Fill))
	entries := []entry{
		{256, long, 1, longs(uint32(w.Cols))},
		{257, long, 1, longs(uint32(w.Rows))},
		{258, short, 1, shorts(16)},
		{259, short, 1, shorts(8)}, // deflate
		{262, short, 1, shorts(1)}, // black is zero
		{273, long, 1, longs(stripOffset)},
		{277, short, 1, shorts(1)},
		{278, long, 1, longs(uint32(w.Rows))},
		{279, long, 1, longs(uint32(strip.Len()))},
		{284, short, 1, shorts(1)},
		{339, short, 1, shorts(2)}, // signed integers
		{33550, double, 3, doubles(math.Abs(t[0]), math.Abs(t[4]), 0)},
		{33922, double, 6, doubles(0, 0, 0, b.West, b.North, 0)},
		// GeoKeys: geographic model, pixels are areas, WGS 84.
		{34735, short, 16, shorts(1, 1, 0, 3, 1024, 0, 1, 2, 1025, 0, 1, 1, 2048, 0, 1, 4326)},
		{42112, ascii, uint32(md.Len() + 1), str(md.String())},
		{42113, ascii, uint32(len(nodata) + 1), str(nodata)},
	}
	ifdSize := uint32(2 + 12*len(entries) + 4)
	extra := ifdOffset + ifdSize

	var out bytes.Buffer
	out.WriteString("II")
	out.Write(shorts(42))
	out.Write(longs(ifdOffset))
	out.Write(strip.Bytes())
	for uint32(out.Len()) < ifdOffset {
		out.WriteByte(0)
	}
	out.Write(shorts(uint16(len(entries))))
	var tail bytes.Buffer
	for _, e := range entries {
		out.Write(shorts(e.tag, e.typ))
		out.Write(longs(e.count))
		if len(e.data) <= 4 {
			field := make([]byte, 4)
			copy(field, e.data)
			out.Write(field)
			continue
		}
		out.Write(longs(extra + uint32(tail.Len())))
		tail.Write(e.data)
		if tail.Len()&1 == 1 {
			tail.WriteByte(0)
		}
	}
	out.Write(longs(0)) // no next IFD
	out.Write(tail.Bytes())
	return out.Bytes(), nil
}
