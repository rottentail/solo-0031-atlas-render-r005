package raster

import (
	"atlas-render/internal/geometry"
	"atlas-render/internal/model"
	"atlas-render/internal/paint"
	"context"
	"image"
	"image/color"
	"image/draw"
	"math"
)

func (p Plan) Render(ctx context.Context) (*image.NRGBA, error) {
	img := image.NewNRGBA(image.Rect(0, 0, p.Width, p.Height))
	bg, _ := paint.Parse(p.Background)
	draw.Draw(img, img.Bounds(), image.NewUniform(bg), image.Point{}, draw.Src)
	for _, op := range p.ops {
		if op.opacity == 0 {
			continue
		}
		for y := op.bounds.Min.Y; y < op.bounds.Max.Y; y++ {
			if e := ctx.Err(); e != nil {
				return nil, e
			}
			for x := op.bounds.Min.X; x < op.bounds.Max.X; x++ {
				world := model.Point{X: (float64(x) + .5) / float64(p.Scale), Y: (float64(y) + .5) / float64(p.Scale)}
				local := op.inverse.Apply(world)
				l := op.layer
				if !geometry.InRect(local, model.Rect{Width: l.Width, Height: l.Height}) {
					continue
				}
				visible := true
				for _, cl := range op.clips {
					if !geometry.InRect(cl.inverse.Apply(world), cl.rect) {
						visible = false
						break
					}
				}
				if !visible {
					continue
				}
				if l.Kind == "ellipse" {
					dx, dy := 2*local.X/l.Width-1, 2*local.Y/l.Height-1
					if dx*dx+dy*dy > 1 {
						continue
					}
				}
				if l.Kind == "polygon" && !geometry.InPolygon(local, l.Points) {
					continue
				}
				alpha := op.opacity
				if op.mask != nil {
					ml := op.mask.inverse.Apply(world)
					if ml.X < 0 || ml.Y < 0 || ml.X >= float64(op.mask.width) || ml.Y >= float64(op.mask.height) {
						continue
					}
					// The placement transform maps each mask texel onto a unit
					// (or scaled) half-open cell in document space; truncating
					// the mapped point gives the zero-origin texel index, which
					// is then offset by the decoded image bounds.
					mbounds := op.mask.bitmap.Bounds()
					ix := int(ml.X)
					iy := int(ml.Y)
					if ix >= op.mask.width {
						ix = op.mask.width - 1
					}
					if iy >= op.mask.height {
						iy = op.mask.height - 1
					}
					mx := mbounds.Min.X + ix
					my := mbounds.Min.Y + iy
					// Read coverage straight from the source. Grayscale and
					// alpha images carry the mask value directly; converting
					// them through NRGBAModel would alpha-associate the
					// channels and distort the luminance ramp.
					var maskCoverage float64
					switch pm := op.mask.bitmap.(type) {
					case *image.Gray:
						maskCoverage = float64(pm.GrayAt(mx, my).Y) / 255
					case *image.Gray16:
						maskCoverage = float64(pm.Gray16At(mx, my).Y) / 65535
					case *image.Alpha:
						maskCoverage = float64(pm.AlphaAt(mx, my).A) / 255
					case *image.Alpha16:
						maskCoverage = float64(pm.Alpha16At(mx, my).A) / 65535
					default:
						mc := color.NRGBAModel.Convert(op.mask.bitmap.At(mx, my)).(color.NRGBA)
						maskCoverage = float64(mc.R+mc.G+mc.B) / (3 * 255)
					}
					if maskCoverage == 0 {
						continue
					}
					alpha *= maskCoverage
					if alpha == 0 {
						continue
					}
				}
				var c color.NRGBA
				if op.bitmap != nil {
					b := op.bitmap.Bounds()
					sx := b.Min.X + int(local.X/l.Width*float64(b.Dx()))
					sy := b.Min.Y + int(local.Y/l.Height*float64(b.Dy()))
					c = color.NRGBAModel.Convert(op.bitmap.At(sx, sy)).(color.NRGBA)
				} else {
					c = paint.Sample(l.Fill, local)
				}
				c.A = uint8(math.Round(float64(c.A) * alpha))
				blend(img, x, y, c)
			}
		}
	}
	return img, nil
}
func blend(img *image.NRGBA, x, y int, s color.NRGBA) {
	d := img.NRGBAAt(x, y)
	sa, da := float64(s.A)/255, float64(d.A)/255
	a := sa + da*(1-sa)
	if a == 0 {
		img.SetNRGBA(x, y, color.NRGBA{})
		return
	}
	mix := func(u, v uint8) uint8 { return uint8(math.Round((float64(u)*sa + float64(v)*da*(1-sa)) / a)) }
	img.SetNRGBA(x, y, color.NRGBA{R: mix(s.R, d.R), G: mix(s.G, d.G), B: mix(s.B, d.B), A: uint8(math.Round(a * 255))})
}
