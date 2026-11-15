package service

import (
	"atlas-render/internal/model"
	"atlas-render/internal/scene"
	"sort"
	"time"
)

// BlockedLayer describes an image layer that cannot be frozen again because
// the asset it references is no longer available.
type BlockedLayer struct {
	LayerID     string   `json:"layerId"`
	Path        []string `json:"path"`
	AssetID     string   `json:"assetId"`
	AssetDigest string   `json:"assetDigest"`
	Reason      string   `json:"reason"`
}

// RecoveryPlan is the read-only preview shown before an archived scene is
// restored. It never mutates state and never touches frozen snapshots.
type RecoveryPlan struct {
	SceneID         string         `json:"sceneId"`
	Revision        int            `json:"revision"`
	State           string         `json:"state"`
	Recoverable     bool           `json:"recoverable"`
	BlockedLayers   []BlockedLayer `json:"blockedLayers"`
	AvailableAssets []string       `json:"availableAssets"`
}

func buildRecoveryPlan(st *model.State, sc model.Scene) RecoveryPlan {
	blocked := []BlockedLayer{}
	blockedAssets := map[string]bool{}
	for _, ref := range scene.ImageReferences(sc.Document) {
		a, ok := st.Assets[ref.AssetID]
		if ok && a.State == "available" {
			continue
		}
		layer := BlockedLayer{LayerID: ref.LayerID, Path: ref.Path, AssetID: ref.AssetID, Reason: "asset retired"}
		if !ok {
			layer.Reason = "asset missing"
		} else {
			layer.AssetDigest = a.Digest
		}
		blocked = append(blocked, layer)
		blockedAssets[ref.AssetID] = true
	}
	available := []string{}
	for id, a := range st.Assets {
		if a.State == "available" {
			available = append(available, id)
		}
	}
	sort.Strings(available)
	return RecoveryPlan{
		SceneID:         sc.ID,
		Revision:        sc.Revision,
		State:           sc.State,
		Recoverable:     len(blocked) == 0,
		BlockedLayers:   blocked,
		AvailableAssets: available,
	}
}

// RecoveryPlan returns a stable preview of what restoring an archived scene
// would require, including every layer blocked by asset retirement and the
// currently usable replacement assets.
func (s *Service) RecoveryPlan(id string) (RecoveryPlan, error) {
	st := s.Store.Read()
	sc, ok := st.Scenes[id]
	if !ok {
		return RecoveryPlan{}, model.Missing("scene not found")
	}
	if sc.State != "archived" {
		return RecoveryPlan{}, model.Conflict("recovery plan is only available for archived scenes")
	}
	return buildRecoveryPlan(&st, sc), nil
}

// Recover restores an archived scene in one committed transaction. When assets
// were retired during archival, replacements maps each blocked asset id to an
// available replacement asset id; every blocked layer must be covered. The
// archived document itself is never rewritten in place: a new revision carries
// the rewritten document, and all existing snapshots keep their captured bytes.
func (s *Service) Recover(id string, expected int, replacements map[string]string) (model.Scene, RecoveryPlan, error) {
	var out model.Scene
	var plan RecoveryPlan
	e := s.Store.Update(func(st *model.State) error {
		sc, ok := st.Scenes[id]
		if !ok {
			return model.Missing("scene not found")
		}
		if sc.State != "archived" {
			return model.Conflict("scene is not archived")
		}
		plan = buildRecoveryPlan(st, sc)
		if sc.Revision != expected {
			// Carry the refreshed plan so the caller can retry without losing context.
			return model.ConflictDetails("scene revision changed", plan)
		}
		blockedAssets := map[string]bool{}
		for _, layer := range plan.BlockedLayers {
			blockedAssets[layer.AssetID] = true
		}
		targets := map[string]bool{}
		for from, to := range replacements {
			if !blockedAssets[from] {
				return model.Invalid("replacement source %q is not a blocked asset", from)
			}
			if !scene.ValidID(to) {
				return model.Invalid("replacement target %q is not a valid asset id", to)
			}
			if from == to {
				return model.Invalid("retired asset %q cannot replace itself", from)
			}
			a, exists := st.Assets[to]
			if !exists || a.State != "available" {
				// A parallel retirement may have changed the picture; refresh the plan.
				plan = buildRecoveryPlan(st, sc)
				return model.ConflictDetails("replacement asset is unavailable", plan)
			}
			if targets[to] {
				return model.Invalid("replacement asset %q cannot serve two retired assets", to)
			}
			targets[to] = true
		}
		if len(replacements) != len(blockedAssets) {
			return model.ConflictDetails("every blocked layer requires an explicit replacement asset", plan)
		}
		doc := scene.ReplaceAssetIDs(sc.Document, replacements)
		if e := scene.Validate(doc); e != nil {
			return e
		}
		for _, ref := range scene.References(doc) {
			a, exists := st.Assets[ref]
			if !exists || a.State != "available" {
				plan = buildRecoveryPlan(st, sc)
				return model.ConflictDetails("scene references unavailable asset", plan)
			}
		}
		sc.Document = doc
		sc.State = "active"
		sc.Revision++
		sc.UpdatedAt = time.Now().UTC()
		st.Scenes[id] = sc
		if len(replacements) > 0 {
			st.Emit("scene.recovered", id)
		} else {
			st.Emit("scene.active", id)
		}
		out = sc
		return nil
	})
	return out, plan, e
}
