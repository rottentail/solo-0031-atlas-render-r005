package service

import (
	"atlas-render/internal/codec"
	"atlas-render/internal/model"
	"atlas-render/internal/scene"
)

// ImportMask registers a reusable grayscale mask. Masks share the same input
// limits as bitmap assets; the same id can only be re-imported with identical
// bytes while still available.
func (s *Service) ImportMask(id string, data []byte) (model.Mask, error) {
	var out model.Mask
	if !scene.ValidID(id) {
		return out, model.Invalid("invalid mask id")
	}
	img, e := codec.Decode(data)
	if e != nil {
		return out, e
	}
	out = model.Mask{ID: id, Digest: model.Digest(data), State: "available", Width: img.Bounds().Dx(), Height: img.Bounds().Dy(), PNG: data}
	e = s.Store.Update(func(st *model.State) error {
		if old, ok := st.Masks[id]; ok {
			if old.Digest == out.Digest && old.State == "available" {
				out = old
				return nil
			}
			return model.Conflict("mask id already exists")
		}
		st.Masks[id] = out
		st.Emit("mask.imported", id)
		return nil
	})
	return out, e
}

// RetireMask deactivates a mask. Retirement blocks new snapshots that reference
// it but never alters bytes already captured in existing snapshots.
func (s *Service) RetireMask(id string) error {
	return s.Store.Update(func(st *model.State) error {
		m, ok := st.Masks[id]
		if !ok {
			return model.Missing("mask not found")
		}
		if m.State == "retired" {
			return nil
		}
		m.State = "retired"
		st.Masks[id] = m
		st.Emit("mask.retired", id)
		return nil
	})
}
