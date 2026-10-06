package api

import (
	"encoding/json"
	"net/http"

	"github.com/arnaubennassar/compainion/internal/domain"
	"github.com/arnaubennassar/compainion/internal/store"
)

// registerSubscriptions wires the subscription resource routes.
func (s *Server) registerSubscriptions() {
	s.handle("GET /subscriptions", s.listSubscriptions)
	s.handle("POST /subscriptions", s.createSubscription)
	s.handle("GET /subscriptions/{id}", s.getSubscription)
	s.handle("DELETE /subscriptions/{id}", s.deleteSubscription)
}

// subscriptionDTO is the wire form: types (array) and filter (object) are
// TEXT-holding-JSON columns in the store.
type subscriptionDTO struct {
	ID        string          `json:"id"`
	Method    string          `json:"method"`
	Target    string          `json:"target"`
	Types     json.RawMessage `json:"types"`
	Filter    json.RawMessage `json:"filter"`
	Secret    *string         `json:"secret,omitempty"`
	Active    bool            `json:"active"`
	CreatedAt string          `json:"created_at"`
	UpdatedAt string          `json:"updated_at"`
}

func subscriptionFromStore(s store.Subscription) subscriptionDTO {
	return subscriptionDTO{
		ID: s.ID, Method: s.Method, Target: s.Target,
		Types: json.RawMessage(s.Types), Filter: json.RawMessage(s.Filter),
		Secret: s.Secret, Active: s.Active, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
	}
}

// subscriptionInput mirrors SubscriptionInput in the spec.
type subscriptionInput struct {
	Method *string         `json:"method"`
	Target *string         `json:"target"`
	Types  []string        `json:"types"`
	Filter json.RawMessage `json:"filter"`
	Secret *string         `json:"secret"`
	Active *bool           `json:"active"`
}

func (s *Server) createSubscription(w http.ResponseWriter, r *http.Request) {
	in, err := decode[subscriptionInput](r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	// Method/target validation happens here (422 for a bad method per the
	// spec's response table; the store's Invalid maps to 400 only).
	if in.Method == nil || (in.Method != nil && *in.Method != "webhook" && *in.Method != "command") {
		writeProblem(w, domain.Errf(domain.Unprocessable, "method must be webhook or command"))
		return
	}
	if in.Target == nil || *in.Target == "" {
		writeProblem(w, domain.Errf(domain.Invalid, "target is required"))
		return
	}

	sub := store.Subscription{
		Method: *in.Method, Target: *in.Target, Secret: in.Secret,
	}
	if len(in.Types) > 0 {
		b, err := json.Marshal(in.Types)
		if err != nil {
			writeProblem(w, err)
			return
		}
		sub.Types = string(b)
	}
	if len(in.Filter) > 0 {
		if err := validateJSONObject(in.Filter); err != nil {
			writeProblem(w, domain.Errf(domain.Invalid, "filter must be a JSON object"))
			return
		}
		sub.Filter = string(in.Filter)
	}

	// Idempotent per (method, target): existing -> 200, new -> 201.
	_, getErr := s.db.GetSubscriptionByTarget(r.Context(), sub.Method, sub.Target)
	existed := getErr == nil

	out, err := s.db.UpsertSubscription(r.Context(), sub)
	if err != nil {
		writeProblem(w, err)
		return
	}
	status := http.StatusCreated
	if existed {
		status = http.StatusOK
	}
	writeJSON(w, status, subscriptionFromStore(out))
}

func (s *Server) listSubscriptions(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := parsePage(r)
	if err != nil {
		writeProblem(w, err)
		return
	}
	subs, err := s.db.ListSubscriptions(r.Context())
	if err != nil {
		writeProblem(w, err)
		return
	}
	subs = paginateAfter(subs, cursor, limit, func(s store.Subscription) string { return s.ID })
	writeJSON(w, http.StatusOK, Envelope{Items: subs, NextCursor: cursorOf(subs, func(s store.Subscription) string { return s.ID })})
}

func (s *Server) getSubscription(w http.ResponseWriter, r *http.Request) {
	sub, err := s.db.GetSubscription(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, subscriptionFromStore(sub))
}

func (s *Server) deleteSubscription(w http.ResponseWriter, r *http.Request) {
	if err := s.db.DeleteSubscription(r.Context(), r.PathValue("id")); err != nil {
		writeProblem(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
