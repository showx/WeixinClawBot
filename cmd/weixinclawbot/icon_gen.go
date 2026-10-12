//go:build ignore

// 生成程序图标。在本目录执行：go run icon_gen.go
package main

import (
	"image"
	"image/color"
	"image/png"
	"os"
)

func main() {
	f, err := os.Create("icon.png")
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err := png.Encode(f, drawIcon(256)); err != nil {
		panic(err)
	}
}

func drawIcon(size int) *image.NRGBA {
	const scale = 4
	big := image.NewNRGBA(image.Rect(0, 0, size*scale, size*scale))
	s := float64(size * scale)
	green := color.NRGBA{R: 7, G: 193, B: 96, A: 255}
	white := color.NRGBA{R: 255, G: 255, B: 255, A: 255}

	fillRoundRect(big, 0.08*s, 0.08*s, 0.84*s, 0.84*s, 0.20*s, green)
	fillRoundRect(big, 0.22*s, 0.22*s, 0.56*s, 0.38*s, 0.11*s, white)
	fillPolygon(big, white, [][2]float64{
		{0.30 * s, 0.56 * s},
		{0.24 * s, 0.74 * s},
		{0.46 * s, 0.58 * s},
	})
	cy := 0.41 * s
	r := 0.055 * s
	for _, cx := range []float64{0.38, 0.50, 0.62} {
		fillCircle(big, cx*s, cy, r, green)
	}
	return downsample(big, size)
}

func fillRoundRect(img *image.NRGBA, x, y, w, h, rad float64, c color.NRGBA) {
	b := img.Bounds()
	for py := b.Min.Y; py < b.Max.Y; py++ {
		for px := b.Min.X; px < b.Max.X; px++ {
			if inRoundRect(float64(px)+0.5, float64(py)+0.5, x, y, w, h, rad) {
				img.SetNRGBA(px, py, c)
			}
		}
	}
}

func inRoundRect(px, py, x, y, w, h, rad float64) bool {
	cx := px
	if cx < x+rad {
		cx = x + rad
	} else if cx > x+w-rad {
		cx = x + w - rad
	}
	cy := py
	if cy < y+rad {
		cy = y + rad
	} else if cy > y+h-rad {
		cy = y + h - rad
	}
	dx, dy := px-cx, py-cy
	return dx*dx+dy*dy <= rad*rad
}

func fillCircle(img *image.NRGBA, cx, cy, r float64, c color.NRGBA) {
	b := img.Bounds()
	for py := b.Min.Y; py < b.Max.Y; py++ {
		for px := b.Min.X; px < b.Max.X; px++ {
			dx := float64(px) + 0.5 - cx
			dy := float64(py) + 0.5 - cy
			if dx*dx+dy*dy <= r*r {
				img.SetNRGBA(px, py, c)
			}
		}
	}
}

func fillPolygon(img *image.NRGBA, c color.NRGBA, pts [][2]float64) {
	minX, minY := pts[0][0], pts[0][1]
	maxX, maxY := minX, minY
	for _, p := range pts[1:] {
		if p[0] < minX {
			minX = p[0]
		}
		if p[1] < minY {
			minY = p[1]
		}
		if p[0] > maxX {
			maxX = p[0]
		}
		if p[1] > maxY {
			maxY = p[1]
		}
	}
	b := img.Bounds()
	x0 := int(minX) - 1
	y0 := int(minY) - 1
	x1 := int(maxX) + 2
	y1 := int(maxY) + 2
	if x0 < b.Min.X {
		x0 = b.Min.X
	}
	if y0 < b.Min.Y {
		y0 = b.Min.Y
	}
	if x1 > b.Max.X {
		x1 = b.Max.X
	}
	if y1 > b.Max.Y {
		y1 = b.Max.Y
	}
	for py := y0; py < y1; py++ {
		for px := x0; px < x1; px++ {
			if pointInPolygon(float64(px)+0.5, float64(py)+0.5, pts) {
				img.SetNRGBA(px, py, c)
			}
		}
	}
}

func pointInPolygon(x, y float64, pts [][2]float64) bool {
	inside := false
	j := len(pts) - 1
	for i := range pts {
		xi, yi := pts[i][0], pts[i][1]
		xj, yj := pts[j][0], pts[j][1]
		if (yi > y) != (yj > y) && x < (xj-xi)*(y-yi)/(yj-yi)+xi {
			inside = !inside
		}
		j = i
	}
	return inside
}

func downsample(src *image.NRGBA, size int) *image.NRGBA {
	scale := src.Bounds().Dx() / size
	out := image.NewNRGBA(image.Rect(0, 0, size, size))
	area := float64(scale * scale)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var r, g, b, a float64
			for sy := 0; sy < scale; sy++ {
				for sx := 0; sx < scale; sx++ {
					c := src.NRGBAAt(x*scale+sx, y*scale+sy)
					r += float64(c.R)
					g += float64(c.G)
					b += float64(c.B)
					a += float64(c.A)
				}
			}
			out.SetNRGBA(x, y, color.NRGBA{
				R: uint8(r/area + 0.5),
				G: uint8(g/area + 0.5),
				B: uint8(b/area + 0.5),
				A: uint8(a/area + 0.5),
			})
		}
	}
	return out
}
