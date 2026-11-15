package service

import (
	"atlas-render/internal/model"
	"atlas-render/internal/persist"
	"atlas-render/internal/raster"
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"path/filepath"
	"testing"
)

func grayPNG(t *testing.T, w, h int, v uint8) []byte {
	t.Helper()
	src := image.NewGray(image.Rect(0, 0, w, h))
	for i := range src.Pix {
		src.Pix[i] = v
	}
	var buf bytes.Buffer
	if e := png.Encode(&buf, src); e != nil {
		t.Fatal(e)
	}
	return buf.Bytes()
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	st, e := persist.Open(filepath.Join(t.TempDir(), "state.json"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &Service{Store: st}
}

func maskedDocument(maskID string) model.Document {
	layer := model.Layer{
		ID: "r", Kind: "rectangle", Width: 4, Height: 4,
		Transform: model.Transform{ScaleX: 1, ScaleY: 1},
		Opacity:   1, Fill: model.Paint{Kind: "solid", Color: "#000000"},
		MaskID: maskID,
	}
	return model.Document{Width: 4, Height: 4, Background: "#FFFFFF", Layers: []model.Layer{layer}}
}

func TestMaskFreezeCapturesAndRetireIsolates(t *testing.T) {
	s := newTestService(t)
	pngBytes := grayPNG(t, 4, 4, 128)
	if _, e := s.ImportMask("fade", pngBytes); e != nil {
		t.Fatalf("import mask: %v", e)
	}
	doc := maskedDocument("fade")
	if _, e := s.PutScene("sc", "masked", 0, doc); e != nil {
		t.Fatalf("put scene: %v", e)
	}
	snap, e := s.Freeze("sc", 1)
	if e != nil {
		t.Fatalf("freeze: %v", e)
	}
	if len(snap.Masks) != 1 || snap.Masks["fade"].Digest != model.Digest(pngBytes) {
		t.Fatalf("snapshot did not capture mask closure: %+v", snap.Masks)
	}

	// Rendering from the frozen snapshot works before retirement.
	plan, e := raster.Compile(snap, 1)
	if e != nil {
		t.Fatalf("compile: %v", e)
	}
	before, e := plan.Render(context.Background())
	if e != nil {
		t.Fatal(e)
	}

	// Retiring the mask blocks new revisions and new freezes...
	if e := s.RetireMask("fade"); e != nil {
		t.Fatalf("retire: %v", e)
	}
	if _, e := s.PutScene("sc", "masked", 1, doc); e == nil {
		t.Fatal("revision referencing retired mask must be rejected")
	}
	if _, e := s.Freeze("sc", 1); e == nil {
		t.Fatal("freeze referencing retired mask must be rejected")
	}
	// ...but the frozen snapshot still renders identical pixels.
	after, e := plan.Render(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			if before.NRGBAAt(x, y) != after.NRGBAAt(x, y) {
				t.Fatalf("frozen pixels changed after mask retirement at (%d,%d)", x, y)
			}
		}
	}
	if c := after.NRGBAAt(0, 0); c.R == 0 || c.R == 255 {
		t.Fatalf("half-coverage mask expected intermediate gray, got %+v", c)
	}
}

func TestMaskImportConflictAndReuse(t *testing.T) {
	s := newTestService(t)
	first := grayPNG(t, 2, 2, 10)
	if _, e := s.ImportMask("m", first); e != nil {
		t.Fatal(e)
	}
	// Identical bytes are idempotent.
	if _, e := s.ImportMask("m", first); e != nil {
		t.Fatalf("identical re-import should be accepted: %v", e)
	}
	// Different bytes under the same id conflict.
	different := grayPNG(t, 2, 2, 20)
	if _, e := s.ImportMask("m", different); e == nil {
		t.Fatal("different content under same mask id must conflict")
	}
	// A dangling mask reference cannot be saved.
	if _, e := s.PutScene("dangling", "x", 0, maskedDocument("ghost")); e == nil {
		t.Fatal("scene referencing missing mask must be rejected")
	}
}

// Guard against accidental changes to grayscale coverage math.
func TestSolidGrayCoverage(t *testing.T) {
	s := newTestService(t)
	if _, e := s.ImportMask("g", grayPNG(t, 1, 1, 255)); e != nil {
		t.Fatal(e)
	}
	doc := maskedDocument("g")
	scene, e := s.PutScene("full", "x", 0, doc)
	if e != nil {
		t.Fatal(e)
	}
	snap, e := s.Freeze("full", scene.Revision)
	if e != nil {
		t.Fatal(e)
	}
	plan, e := raster.Compile(snap, 1)
	if e != nil {
		t.Fatal(e)
	}
	img, e := plan.Render(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if c := img.NRGBAAt(0, 0); c.R != 0 || color.NRGBA(c) != (color.NRGBA{R: 0, G: 0, B: 0, A: 255}) {
		t.Fatalf("full-coverage mask should reveal black layer: %+v", c)
	}
}
