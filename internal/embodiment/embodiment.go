// Package embodiment provides a body adapter. Body fields are NOT hard-coded:
// the schema is declared in YAML (config.BodyField) and the live values come
// from a BodySource (typically the environment). This keeps proprioception
// generic so a new embodiment can add fields (e.g. temperature, battery) without
// code changes.
package embodiment

import (
	"context"

	"liveagent/internal/config"
	"liveagent/internal/domain"
)

// BodySource supplies the current body state. Environments that simulate a body
// implement this.
type BodySource interface {
	BodyState() map[string]any
}

// Adapter is a schema-driven Embodiment.
type Adapter struct {
	source       BodySource
	fields       []config.BodyField
	capabilities []domain.Capability
	healthFields []string
}

// New builds an Adapter from the body schema and a live source.
func New(cfg config.BodyConfig, source BodySource, caps []domain.Capability) *Adapter {
	health := []string{}
	for _, f := range cfg.Fields {
		if f.Name == "health" || f.Name == "energy" {
			health = append(health, f.Name)
		}
	}
	return &Adapter{source: source, fields: cfg.Fields, capabilities: caps, healthFields: health}
}

// Capabilities implements domain.Embodiment.
func (a *Adapter) Capabilities(_ context.Context) []domain.Capability { return a.capabilities }

// Proprioception returns the full body state, filtered to declared fields so the
// schema is authoritative.
func (a *Adapter) Proprioception(_ context.Context) (map[string]any, error) {
	live := a.source.BodyState()
	out := map[string]any{}
	if len(a.fields) == 0 {
		// No schema declared: pass through everything.
		for k, v := range live {
			out[k] = v
		}
		return out, nil
	}
	for _, f := range a.fields {
		if v, ok := live[f.Name]; ok {
			out[f.Name] = v
		} else {
			out[f.Name] = f.Initial
		}
	}
	return out, nil
}

// Health returns the health-relevant subset of proprioception.
func (a *Adapter) Health(ctx context.Context) (map[string]any, error) {
	full, err := a.Proprioception(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	for _, name := range a.healthFields {
		if v, ok := full[name]; ok {
			out[name] = v
		}
	}
	return out, nil
}
