// Package gridworld is a self-contained example Environment used to validate the
// framework without a real LLM or external services. It simulates a 2D map with
// unknown regions, resources, obstacles, and a body (energy/health/age). It is a
// concrete Environment + BodySource; the runtime only talks to it through
// interfaces.
package gridworld

import (
	"context"
	"math/rand"
	"sync"

	"liveagent/internal/domain"
)

type cell struct{ x, y int }

// GridWorld implements domain.Environment and embodiment.BodySource.
type GridWorld struct {
	mu sync.Mutex

	width, height int
	obstacles     map[cell]bool
	resources     map[cell]bool
	explored      map[cell]bool

	pos cell

	energy, maxEnergy float64
	health, maxHealth float64
	age               int64
	maxLifespan       int64

	moveCost, exploreCost, observeCost float64
	restGain, collectGain              float64

	tick          int64
	episodeStart  int64
	episodeLength int64
	exploreGoal   float64

	collectedThisEpisode int
	rng                  *rand.Rand
}

// Params configures a GridWorld. Zero values fall back to sensible defaults.
type Params struct {
	Width, Height int
	NumResources  int
	NumObstacles  int
	Seed          int64
	InitEnergy    float64
	MaxEnergy     float64
	InitHealth    float64
	MaxHealth     float64
	MaxLifespan   int64
	MoveCost      float64
	ExploreCost   float64
	ObserveCost   float64
	RestGain      float64
	CollectGain   float64
	EpisodeLength int64
	ExploreGoal   float64
}

// New builds a GridWorld from params, placing resources/obstacles deterministically.
func New(p Params) *GridWorld {
	def := func(v float64, d float64) float64 {
		if v == 0 {
			return d
		}
		return v
	}
	defi := func(v, d int) int {
		if v == 0 {
			return d
		}
		return v
	}
	defi64 := func(v, d int64) int64 {
		if v == 0 {
			return d
		}
		return v
	}

	g := &GridWorld{
		width:         defi(p.Width, 6),
		height:        defi(p.Height, 6),
		obstacles:     map[cell]bool{},
		resources:     map[cell]bool{},
		explored:      map[cell]bool{},
		maxEnergy:     def(p.MaxEnergy, 100),
		maxHealth:     def(p.MaxHealth, 100),
		maxLifespan:   p.MaxLifespan, // 0 = unlimited
		moveCost:      def(p.MoveCost, 8),
		exploreCost:   def(p.ExploreCost, 12),
		observeCost:   def(p.ObserveCost, 1),
		restGain:      def(p.RestGain, 20),
		collectGain:   def(p.CollectGain, 45),
		episodeLength: defi64(p.EpisodeLength, 14),
		exploreGoal:   def(p.ExploreGoal, 0.6),
		rng:           rand.New(rand.NewSource(defi64(p.Seed, 42))),
	}
	g.energy = def(p.InitEnergy, 100)
	g.health = def(p.InitHealth, 100)
	g.placeFeatures(defi(p.NumObstacles, 3), defi(p.NumResources, 4))
	g.pos = cell{0, 0}
	g.reveal(g.pos)
	return g
}

func (g *GridWorld) placeFeatures(nObs, nRes int) {
	free := func() cell {
		for {
			c := cell{g.rng.Intn(g.width), g.rng.Intn(g.height)}
			if c == (cell{0, 0}) || g.obstacles[c] || g.resources[c] {
				continue
			}
			return c
		}
	}
	for i := 0; i < nObs && i < g.width*g.height/3; i++ {
		g.obstacles[free()] = true
	}
	for i := 0; i < nRes && i < g.width*g.height/3; i++ {
		g.resources[free()] = true
	}
}

// Tick returns the current tick counter.
func (g *GridWorld) Tick() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.tick
}

// AvailableActions returns the action schemas the agent may use. These become the
// registry whitelist.
func (g *GridWorld) AvailableActions(_ context.Context) []domain.ActionSchema {
	return []domain.ActionSchema{
		{Name: "observe", Description: "Look around and reveal adjacent cells.", Parameters: map[string]domain.ParamSpec{}},
		{Name: "move", Description: "Move one cell in a direction.", Parameters: map[string]domain.ParamSpec{
			"direction": {Type: "string", Required: true, Description: "up|down|left|right"},
		}},
		{Name: "explore", Description: "Move toward the nearest unknown region.", Parameters: map[string]domain.ParamSpec{}},
		{Name: "collect", Description: "Collect a resource on the current cell.", Parameters: map[string]domain.ParamSpec{}},
		{Name: "rest", Description: "Rest to recover energy.", Parameters: map[string]domain.ParamSpec{}},
	}
}

// BodyState implements embodiment.BodySource.
func (g *GridWorld) BodyState() map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	return map[string]any{
		"energy": g.energy,
		"health": g.health,
		"age":    float64(g.age),
	}
}
