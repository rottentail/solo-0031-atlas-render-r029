package service

import (
	"atlas-render/internal/model"
	"atlas-render/internal/persist"
	"bytes"
	"image"
	"image/color"
	"image/png"
	"path/filepath"
	"sort"
	"sync"
	"testing"
)

func pngBytes(t *testing.T, r, g, b uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			img.Set(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
		}
	}
	var buf bytes.Buffer
	if e := png.Encode(&buf, img); e != nil {
		t.Fatal(e)
	}
	return buf.Bytes()
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	store, e := persist.Open(filepath.Join(t.TempDir(), "state.json"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &Service{Store: store}
}

func imageLayer(id, asset string, x float64) model.Layer {
	return model.Layer{ID: id, Kind: "image", Width: 10, Height: 10,
		AssetID: asset, Opacity: 1,
		Transform: model.Transform{X: x, Y: 1, ScaleX: 1, ScaleY: 1}}
}

func mustImport(t *testing.T, s *Service, id string, data []byte) {
	t.Helper()
	if _, e := s.ImportAsset(id, data); e != nil {
		t.Fatalf("import %s: %v", id, e)
	}
}

func mustPut(t *testing.T, s *Service, doc model.Document, expected int) model.Scene {
	t.Helper()
	sc, e := s.PutScene("emblem", "印记", expected, doc)
	if e != nil {
		t.Fatalf("put scene: %v", e)
	}
	return sc
}

func sampleDoc(assetA, assetB string) model.Document {
	group := model.Layer{ID: "grp", Kind: "group", Opacity: 1,
		Transform: model.Transform{X: 1, Y: 1, ScaleX: 1, ScaleY: 1},
		Clip:      &model.Rect{X: 0, Y: 0, Width: 50, Height: 50},
		Children:  []model.Layer{imageLayer("nested-img", assetB, 4)}}
	return model.Document{Width: 100, Height: 100, Background: "#112233",
		Layers: []model.Layer{imageLayer("top-img", assetA, 2), group}}
}

func archivedScene(t *testing.T, s *Service, doc model.Document) model.Scene {
	sc := mustPut(t, s, doc, 0)
	sc, e := s.SceneState("emblem", sc.Revision, "archived")
	if e != nil {
		t.Fatalf("archive: %v", e)
	}
	return sc
}

func TestRecoveryPlanReportsNestedBlockedLayers(t *testing.T) {
	s := newTestService(t)
	mustImport(t, s, "alpha", pngBytes(t, 1, 2, 3))
	mustImport(t, s, "beta", pngBytes(t, 4, 5, 6))
	sc := archivedScene(t, s, sampleDoc("alpha", "beta"))
	if e := s.RetireAsset("alpha"); e != nil {
		t.Fatal(e)
	}
	plan, e := s.RecoveryPlan("emblem")
	if e != nil {
		t.Fatal(e)
	}
	if plan.Recoverable || plan.Revision != sc.Revision || plan.State != "archived" {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	if len(plan.BlockedLayers) != 1 {
		t.Fatalf("want one blocked layer, got %+v", plan.BlockedLayers)
	}
	blocked := plan.BlockedLayers[0]
	if blocked.LayerID != "top-img" || blocked.AssetID != "alpha" || blocked.Reason != "asset retired" {
		t.Fatalf("unexpected blocked layer: %+v", blocked)
	}
	gotAvailable := append([]string{}, plan.AvailableAssets...)
	sort.Strings(gotAvailable)
	want := []string{"beta"}
	if len(gotAvailable) != len(want) || gotAvailable[0] != want[0] {
		t.Fatalf("available assets = %v, want %v", gotAvailable, want)
	}
}

func TestRecoverWithoutRetirementKeepsDocument(t *testing.T) {
	s := newTestService(t)
	mustImport(t, s, "alpha", pngBytes(t, 1, 2, 3))
	mustImport(t, s, "beta", pngBytes(t, 4, 5, 6))
	sc := archivedScene(t, s, sampleDoc("alpha", "beta"))
	got, plan, e := s.Recover("emblem", sc.Revision, nil)
	if e != nil {
		t.Fatal(e)
	}
	if got.State != "active" || got.Revision != sc.Revision+1 {
		t.Fatalf("unexpected recovered scene: %+v", got)
	}
	if !plan.Recoverable || len(plan.BlockedLayers) != 0 {
		t.Fatalf("plan should be clear: %+v", plan)
	}
	refs := map[string]bool{}
	var scanRefs func([]model.Layer)
	scanRefs = func(ls []model.Layer) {
		for _, l := range ls {
			if l.Kind == "image" {
				refs[l.AssetID] = true
			}
			scanRefs(l.Children)
		}
	}
	scanRefs(got.Document.Layers)
	if !refs["alpha"] || !refs["beta"] {
		t.Fatalf("document references changed: %v", refs)
	}
}

func TestRecoverRequiresEveryBlockedAsset(t *testing.T) {
	s := newTestService(t)
	mustImport(t, s, "alpha", pngBytes(t, 1, 2, 3))
	mustImport(t, s, "beta", pngBytes(t, 4, 5, 6))
	mustImport(t, s, "gamma", pngBytes(t, 7, 8, 9))
	sc := archivedScene(t, s, sampleDoc("alpha", "beta"))
	if e := s.RetireAsset("alpha"); e != nil {
		t.Fatal(e)
	}
	// Missing mapping: no partial state is committed.
	if _, _, e := s.Recover("emblem", sc.Revision, map[string]string{}); e == nil {
		t.Fatal("expected incomplete replacements to fail")
	}
	cur := s.Store.Read().Scenes["emblem"]
	if cur.State != "archived" || cur.Revision != sc.Revision {
		t.Fatalf("failure altered state: %+v", cur)
	}
	// A source that is not blocked is rejected.
	if _, _, e := s.Recover("emblem", sc.Revision,
		map[string]string{"alpha": "gamma", "beta": "gamma"}); e == nil {
		t.Fatal("expected unblocked source and duplicate target to fail")
	}
}

func TestRecoverRewritesNestedLayersAndPreservesSnapshots(t *testing.T) {
	s := newTestService(t)
	alphaPng := pngBytes(t, 1, 2, 3)
	betaPng := pngBytes(t, 4, 5, 6)
	gammaPng := pngBytes(t, 7, 8, 9)
	mustImport(t, s, "alpha", alphaPng)
	mustImport(t, s, "beta", betaPng)
	mustImport(t, s, "gamma", gammaPng)
	doc := sampleDoc("alpha", "beta")
	sc := mustPut(t, s, doc, 0)
	// Freeze before archival: the snapshot closure captures alpha bytes.
	snap, e := s.Freeze("emblem", sc.Revision)
	if e != nil {
		t.Fatal(e)
	}
	sc, _ = s.SceneState("emblem", sc.Revision, "archived")
	if e := s.RetireAsset("alpha"); e != nil {
		t.Fatal(e)
	}
	if e := s.RetireAsset("beta"); e != nil {
		t.Fatal(e)
	}
	plan, e := s.RecoveryPlan("emblem")
	if e != nil {
		t.Fatal(e)
	}
	if len(plan.BlockedLayers) != 2 {
		t.Fatalf("want two blocked layers, got %+v", plan.BlockedLayers)
	}
	nested := plan.BlockedLayers[0]
	if nested.LayerID != "top-img" {
		t.Fatalf("document order broken: %+v", plan.BlockedLayers)
	}
	if path := plan.BlockedLayers[1].Path; len(path) != 2 || path[0] != "grp" || path[1] != "nested-img" {
		t.Fatalf("nested path wrong: %v", path)
	}
	got, _, e := s.Recover("emblem", sc.Revision,
		map[string]string{"alpha": "gamma", "beta": "gamma"})
	if e == nil {
		t.Fatal("two retired assets cannot share one replacement")
	}
	// Distinct replacements are required.
	deltaPng := pngBytes(t, 9, 9, 9)
	mustImport(t, s, "delta", deltaPng)
	got, _, e = s.Recover("emblem", sc.Revision,
		map[string]string{"alpha": "gamma", "beta": "delta"})
	if e != nil {
		t.Fatalf("recover: %v", e)
	}
	if got.State != "active" {
		t.Fatalf("scene not active: %+v", got)
	}
	walk := map[string]string{}
	var scan func([]model.Layer)
	scan = func(ls []model.Layer) {
		for _, l := range ls {
			if l.Kind == "image" {
				walk[l.ID] = l.AssetID
			}
			scan(l.Children)
		}
	}
	scan(got.Document.Layers)
	if walk["top-img"] != "gamma" || walk["nested-img"] != "delta" {
		t.Fatalf("replacement rewrite wrong: %v", walk)
	}
	// Old snapshot is byte-identical and still holds the retired alpha closure.
	persisted := s.Store.Read().Snapshots[snap.ID]
	if persisted.Digest != snap.Digest {
		t.Fatal("snapshot digest changed after recovery")
	}
	if a := persisted.Assets["alpha"]; a.Digest != model.Digest(alphaPng) {
		t.Fatal("snapshot lost captured retired asset bytes")
	}
	// New freeze succeeds against the recovered document.
	again, e := s.Freeze("emblem", got.Revision)
	if e != nil {
		t.Fatalf("freeze after recovery: %v", e)
	}
	if _, ok := again.Assets["alpha"]; ok {
		t.Fatal("new snapshot must not reference the retired asset")
	}
}

func TestRecoverRejectsParallelRevisionAndRetirement(t *testing.T) {
	s := newTestService(t)
	mustImport(t, s, "alpha", pngBytes(t, 1, 2, 3))
	mustImport(t, s, "beta", pngBytes(t, 4, 5, 6))
	mustImport(t, s, "gamma", pngBytes(t, 7, 8, 9))
	sc := archivedScene(t, s, sampleDoc("alpha", "beta"))
	if e := s.RetireAsset("alpha"); e != nil {
		t.Fatal(e)
	}
	// Another actor recovers first; the stale confirmation must not overwrite it.
	first, _, e := s.Recover("emblem", sc.Revision, map[string]string{"alpha": "beta"})
	if e != nil {
		t.Fatal(e)
	}
	_, _, e = s.Recover("emblem", sc.Revision, map[string]string{"alpha": "beta"})
	de, ok := e.(*model.Error)
	if !ok || de.Status != 409 {
		t.Fatalf("want 409 conflict for stale confirmation, got %v", e)
	}
	cur := s.Store.Read().Scenes["emblem"]
	if cur.Revision != first.Revision {
		t.Fatal("stale confirmation changed committed revision")
	}

	// Re-archive and retire the replacement target; a stale revision while still
	// archived returns the refreshed blocked-layer context in the 409 details.
	sc2, e := s.SceneState("emblem", first.Revision, "archived")
	if e != nil {
		t.Fatal(e)
	}
	if e := s.RetireAsset("beta"); e != nil {
		t.Fatal(e)
	}
	_, _, e = s.Recover("emblem", sc.Revision, map[string]string{"beta": "gamma"})
	de, ok = e.(*model.Error)
	if !ok || de.Status != 409 || de.Details == nil {
		t.Fatalf("want stale-revision 409 with refreshed details, got %v", e)
	}
	plan, e := s.RecoveryPlan("emblem")
	if e != nil {
		t.Fatal(e)
	}
	if plan.BlockedLayers[0].AssetID != "beta" {
		t.Fatalf("refreshed plan should show beta blocked: %+v", plan.BlockedLayers)
	}
	got, _, e := s.Recover("emblem", sc2.Revision, map[string]string{"beta": "gamma"})
	if e != nil {
		t.Fatalf("recover with current revision after target retirement: %v", e)
	}
	if got.State != "active" {
		t.Fatalf("scene not active: %+v", got)
	}
}

func TestRecoverConcurrentConfirmationsSerialize(t *testing.T) {
	s := newTestService(t)
	mustImport(t, s, "alpha", pngBytes(t, 1, 2, 3))
	mustImport(t, s, "beta", pngBytes(t, 4, 5, 6))
	sc := archivedScene(t, s, sampleDoc("alpha", "beta"))
	if e := s.RetireAsset("alpha"); e != nil {
		t.Fatal(e)
	}
	const n = 8
	var wg sync.WaitGroup
	results := make(chan error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, e := s.Recover("emblem", sc.Revision, map[string]string{"alpha": "beta"})
			results <- e
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	okCount, conflictCount := 0, 0
	for e := range results {
		switch {
		case e == nil:
			okCount++
		default:
			de, isDomain := e.(*model.Error)
			if !isDomain || de.Status != 409 {
				t.Fatalf("unexpected error: %v", e)
			}
			conflictCount++
		}
	}
	if okCount != 1 || conflictCount != n-1 {
		t.Fatalf("want exactly one success, got ok=%d conflict=%d", okCount, conflictCount)
	}
	cur := s.Store.Read().Scenes["emblem"]
	if cur.State != "active" || cur.Revision != sc.Revision+1 {
		t.Fatalf("unexpected final scene: %+v", cur)
	}
}
