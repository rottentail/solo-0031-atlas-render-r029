package scene

import (
	"atlas-render/internal/model"
	"atlas-render/internal/paint"
	"math"
	"regexp"
)

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$`)

func ValidID(id string) bool { return identifier.MatchString(id) }
func finite(v float64) bool  { return !math.IsNaN(v) && !math.IsInf(v, 0) && math.Abs(v) <= 100000 }
func Validate(d model.Document) error {
	if d.Width < 1 || d.Height < 1 || d.Width > 1536 || d.Height > 1536 {
		return model.Invalid("canvas dimensions must be 1..1536")
	}
	if _, e := paint.Parse(d.Background); e != nil {
		return model.Invalid("background: %v", e)
	}
	seen := map[string]bool{}
	count := 0
	var walk func([]model.Layer, int) error
	walk = func(layers []model.Layer, depth int) error {
		if depth > 8 {
			return model.Invalid("scene nesting exceeds eight")
		}
		for _, l := range layers {
			count++
			if count > 200 {
				return model.Invalid("scene exceeds 200 layers")
			}
			if !ValidID(l.ID) || seen[l.ID] {
				return model.Invalid("layer id invalid or repeated: %s", l.ID)
			}
			seen[l.ID] = true
			t := l.Transform
			for _, n := range []float64{l.Width, l.Height, l.Opacity, t.X, t.Y, t.ScaleX, t.ScaleY, t.Degrees} {
				if !finite(n) {
					return model.Invalid("layer %s: nonfinite or unbounded geometry", l.ID)
				}
			}
			if l.Opacity < 0 || l.Opacity > 1 || math.Abs(t.ScaleX) < .001 || math.Abs(t.ScaleY) < .001 || math.Abs(t.ScaleX) > 100 || math.Abs(t.ScaleY) > 100 {
				return model.Invalid("layer %s: invalid opacity or scale", l.ID)
			}
			if l.Clip != nil {
				r := l.Clip
				if !finite(r.X) || !finite(r.Y) || !finite(r.Width) || !finite(r.Height) || r.Width <= 0 || r.Height <= 0 {
					return model.Invalid("layer %s: invalid clip", l.ID)
				}
			}
			if l.Kind == "group" {
				if len(l.Children) == 0 {
					return model.Invalid("group %s is empty", l.ID)
				}
				if e := walk(l.Children, depth+1); e != nil {
					return e
				}
				continue
			}
			if len(l.Children) > 0 {
				return model.Invalid("only groups can contain layers")
			}
			if l.Width <= 0 || l.Height <= 0 || l.Width > 4096 || l.Height > 4096 {
				return model.Invalid("layer %s: invalid dimensions", l.ID)
			}
			switch l.Kind {
			case "rectangle", "ellipse":
			case "polygon":
				if len(l.Points) < 3 || len(l.Points) > 128 {
					return model.Invalid("polygon requires 3..128 points")
				}
				for _, p := range l.Points {
					if !finite(p.X) || !finite(p.Y) || p.X < 0 || p.X > l.Width || p.Y < 0 || p.Y > l.Height {
						return model.Invalid("polygon point outside local bounds")
					}
				}
			case "image":
				if !ValidID(l.AssetID) {
					return model.Invalid("image requires asset id")
				}
				continue
			default:
				return model.Invalid("layer %s: unknown kind", l.ID)
			}
			if l.Fill.Kind != "solid" && l.Fill.Kind != "linear" {
				return model.Invalid("unknown paint kind")
			}
			if _, e := paint.Parse(l.Fill.Color); e != nil {
				return model.Invalid("invalid fill color")
			}
			if l.Fill.Kind == "linear" {
				if _, e := paint.Parse(l.Fill.EndColor); e != nil {
					return model.Invalid("invalid gradient color")
				}
				for _, n := range []float64{l.Fill.From.X, l.Fill.From.Y, l.Fill.To.X, l.Fill.To.Y} {
					if !finite(n) {
						return model.Invalid("invalid gradient endpoint")
					}
				}
				if l.Fill.From == l.Fill.To {
					return model.Invalid("gradient endpoints coincide")
				}
			}
		}
		return nil
	}
	return walk(d.Layers, 0)
}
func References(d model.Document) []string {
	seen := map[string]bool{}
	out := []string{}
	var walk func([]model.Layer)
	walk = func(ls []model.Layer) {
		for _, l := range ls {
			if l.Kind == "image" && !seen[l.AssetID] {
				seen[l.AssetID] = true
				out = append(out, l.AssetID)
			}
			walk(l.Children)
		}
	}
	walk(d.Layers)
	return out
}

// ImageReference identifies one image layer and the group path that contains it.
type ImageReference struct {
	LayerID string   `json:"layerId"`
	AssetID string   `json:"assetId"`
	Path    []string `json:"path"`
}

// ImageReferences lists every image layer in document order with its ancestor path.
func ImageReferences(d model.Document) []ImageReference {
	out := []ImageReference{}
	var walk func([]model.Layer, []string)
	walk = func(ls []model.Layer, path []string) {
		for _, l := range ls {
			if l.Kind == "image" {
				out = append(out, ImageReference{LayerID: l.ID, AssetID: l.AssetID, Path: append(append([]string{}, path...), l.ID)})
			}
			walk(l.Children, append(append([]string{}, path...), l.ID))
		}
	}
	walk(d.Layers, nil)
	return out
}

// ReplaceAssetIDs returns a copy of the document with every reference to a key
// in replacements rewritten to its mapped asset id. Non-image layers are untouched.
func ReplaceAssetIDs(d model.Document, replacements map[string]string) model.Document {
	var walk func([]model.Layer)
	walk = func(ls []model.Layer) {
		for i := range ls {
			if ls[i].Kind == "image" {
				if id, ok := replacements[ls[i].AssetID]; ok {
					ls[i].AssetID = id
				}
			}
			if len(ls[i].Children) > 0 {
				walk(ls[i].Children)
			}
		}
	}
	next := model.Clone(d)
	walk(next.Layers)
	return next
}
