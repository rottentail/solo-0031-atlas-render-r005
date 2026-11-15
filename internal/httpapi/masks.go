package httpapi

import (
	"atlas-render/internal/model"
	"io"
	"net/http"
)

func (a API) importMask(w http.ResponseWriter, r *http.Request) {
	b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if e != nil {
		fail(w, model.Invalid("mask body too large"))
		return
	}
	v, e := a.Service.ImportMask(r.PathValue("id"), b)
	if e != nil {
		fail(w, e)
		return
	}
	write(w, 201, v)
}

func (a API) retireMask(w http.ResponseWriter, r *http.Request) {
	if e := a.Service.RetireMask(r.PathValue("id")); e != nil {
		fail(w, e)
		return
	}
	write(w, 200, map[string]string{"state": "retired"})
}

func (a API) getMask(w http.ResponseWriter, r *http.Request) {
	v, ok := a.Service.Store.Read().Masks[r.PathValue("id")]
	if !ok {
		fail(w, model.Missing("mask not found"))
		return
	}
	write(w, 200, v)
}
