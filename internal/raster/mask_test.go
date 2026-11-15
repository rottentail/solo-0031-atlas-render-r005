package raster

import (
	"atlas-render/internal/codec"
	"atlas-render/internal/geometry"
	"atlas-render/internal/model"
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// gradientMask builds an 8x1 grayscale PNG: white on the left fading to black
// on the right, and returns the encoded snapshot-ready mask record.
func gradientMask(t *testing.T, id string) model.Mask {
	t.Helper()
	src := image.NewGray(image.Rect(0, 0, 8, 1))
	for x := 0; x < 8; x++ {
		src.SetGray(x, 0, color.Gray{Y: uint8(255 * x / 7)})
	}
	var buf bytes.Buffer
	if e := png.Encode(&buf, src); e != nil {
		t.Fatal(e)
	}
	return model.Mask{ID: id, Digest: model.Digest(buf.Bytes()), State: "available", Width: 8, Height: 1, PNG: buf.Bytes()}
}

func solidLayer(id string, fill string) model.Layer {
	return model.Layer{
		ID: id, Kind: "rectangle", Width: 8, Height: 1,
		Transform: model.Transform{ScaleX: 1, ScaleY: 1},
		Opacity:   1, Fill: model.Paint{Kind: "solid", Color: fill},
	}
}

func TestMaskModulatesAlpha(t *testing.T) {
	mk := gradientMask(t, "fade")
	l := solidLayer("r", "#000000")
	l.MaskID = "fade"
	// Identity placement: each mask texel covers one document-pixel-wide cell.
	l.MaskTransform = &model.Transform{ScaleX: 1, ScaleY: 1}
	snap := model.Snapshot{
		Document: model.Document{Width: 8, Height: 1, Background: "#FFFFFF", Layers: []model.Layer{l}},
		Masks:    map[string]model.Mask{"fade": mk},
	}
	plan, e := Compile(snap, 1)
	if e != nil {
		t.Fatalf("compile: %v", e)
	}
	img, e := plan.Render(context.Background())
	if e != nil {
		t.Fatalf("render: %v", e)
	}
	// Leftmost pixel samples texel 0 (mask value 0) -> stays the white background.
	if c := img.NRGBAAt(0, 0); c.R != 255 || c.A != 255 {
		t.Fatalf("masked-off pixel changed: %+v", c)
	}
	// Rightmost pixel samples texel 7 (mask value 255) -> fully replaced by black.
	if c := img.NRGBAAt(7, 0); c.R != 0 || c.A != 255 {
		t.Fatalf("fully-masked pixel not black: %+v", c)
	}
	// Interior pixels must show a monotonic, non-trivial ramp (soft mask).
	prev := uint8(255)
	for x := 1; x < 7; x++ {
		c := img.NRGBAAt(x, 0)
		if c.R >= prev {
			t.Fatalf("expected darkening ramp, x=%d %+v after %d", x, c, prev)
		}
		prev = c.R
	}
}

func TestMaskSharedAndDeterministic(t *testing.T) {
	mk := gradientMask(t, "shared")
	first := solidLayer("a", "#0000FF")
	first.MaskID = "shared"
	second := solidLayer("b", "#FF0000")
	second.MaskID = "shared"
	snap := model.Snapshot{
		Document: model.Document{Width: 8, Height: 1, Background: "#FFFFFF", Layers: []model.Layer{first, second}},
		Masks:    map[string]model.Mask{"shared": mk},
	}
	plan, e := Compile(snap, 1)
	if e != nil {
		t.Fatalf("compile: %v", e)
	}
	if len(plan.ops) != 2 || plan.ops[0].mask == nil || plan.ops[1].mask == nil {
		t.Fatal("both layers must bind the shared mask")
	}
	// Both bindings decode the very same image: reuse does not duplicate bytes.
	if plan.ops[0].mask.bitmap != plan.ops[1].mask.bitmap {
		t.Fatal("shared mask decoded more than once")
	}
	img1, e := plan.Render(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	img2, e := plan.Render(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	for x := 0; x < 8; x++ {
		if img1.NRGBAAt(x, 0) != img2.NRGBAAt(x, 0) {
			t.Fatalf("non-deterministic output at x=%d", x)
		}
	}
}

func TestMaskComposesWithClip(t *testing.T) {
	mk := gradientMask(t, "fade")
	l := solidLayer("r", "#000000")
	l.MaskID = "fade"
	// Hard clip keeps only local x in [2,4); outside the clip the layer is
	// removed even where the soft mask would be permissive.
	l.Clip = &model.Rect{X: 2, Y: 0, Width: 2, Height: 1}
	snap := model.Snapshot{
		Document: model.Document{Width: 8, Height: 1, Background: "#FFFFFF", Layers: []model.Layer{l}},
		Masks:    map[string]model.Mask{"fade": mk},
	}
	plan, e := Compile(snap, 1)
	if e != nil {
		t.Fatalf("compile: %v", e)
	}
	img, e := plan.Render(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	for x := 0; x < 8; x++ {
		c := img.NRGBAAt(x, 0)
		inClip := x >= 2 && x < 4
		if inClip && c.R == 255 {
			t.Fatalf("clipped+masked region erased at x=%d", x)
		}
		if !inClip && (c.R != 255 || c.G != 255 || c.B != 255) {
			t.Fatalf("clip leaked at x=%d: %+v", x, c)
		}
	}
}

func TestMaskOutsideCoverageHides(t *testing.T) {
	mk := gradientMask(t, "fade")
	l := solidLayer("r", "#000000")
	l.MaskID = "fade"
	// Place the mask entirely off-canvas; every layer pixel must remain the
	// background because points outside mask coverage are fully masked.
	l.MaskTransform = &model.Transform{X: 100, ScaleX: 1, ScaleY: 1}
	snap := model.Snapshot{
		Document: model.Document{Width: 8, Height: 1, Background: "#FFFFFF", Layers: []model.Layer{l}},
		Masks:    map[string]model.Mask{"fade": mk},
	}
	plan, e := Compile(snap, 1)
	if e != nil {
		t.Fatalf("compile: %v", e)
	}
	img, e := plan.Render(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	for x := 0; x < 8; x++ {
		if c := img.NRGBAAt(x, 0); c.R != 255 || c.G != 255 || c.B != 255 {
			t.Fatalf("off-canvas mask did not hide pixel x=%d: %+v", x, c)
		}
	}
}

func TestMaskTransformInverseSanity(t *testing.T) {
	// Guard the geometry helper used to map world points back into mask space.
	m := geometry.From(model.Transform{X: 10, ScaleX: 2, ScaleY: 3})
	inv, ok := m.Inverse()
	if !ok {
		t.Fatal("inverse failed")
	}
	p := inv.Apply(m.Apply(model.Point{X: 4, Y: 5}))
	if d := p.X - 4; d > 1e-9 || d < -1e-9 {
		t.Fatalf("round trip broken: %+v", p)
	}
}

// Ensure the codec still accepts the grayscale PNG used by masks.
func TestCodecDecodesGrayscale(t *testing.T) {
	mk := gradientMask(t, "fade")
	img, e := codec.Decode(mk.PNG)
	if e != nil {
		t.Fatalf("decode: %v", e)
	}
	if img.Bounds().Dx() != 8 || img.Bounds().Dy() != 1 {
		t.Fatalf("unexpected bounds %v", img.Bounds())
	}
}
