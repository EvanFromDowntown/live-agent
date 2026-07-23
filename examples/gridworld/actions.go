package gridworld

import (
	"context"

	"liveagent/internal/domain"
)

// Observe implements domain.Environment.
func (g *GridWorld) Observe(_ context.Context) (domain.Observation, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.observation(), nil
}

// Execute applies an action, updates the body/world, and returns the outcome
// with a multi-dimensional reward. Illegal directions or actions off-target are
// safe no-ops (the environment never crashes on bad input).
func (g *GridWorld) Execute(_ context.Context, action domain.Action) (domain.Outcome, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.tick++
	g.age++

	reward := domain.RewardVector{}
	info := map[string]any{}
	text := ""

	switch action.Name {
	case "observe":
		revealed := g.revealAround(g.pos)
		g.spend(g.observeCost, &reward)
		reward.InformationGain = float64(revealed)
		text = "observed surroundings"

	case "move":
		dir, _ := action.Parameters["direction"].(string)
		moved, revealed := g.tryMove(dir)
		g.spend(g.moveCost, &reward)
		reward.InformationGain = float64(revealed)
		if moved {
			text = "moved " + dir
		} else {
			reward.RiskCost += 0.2
			text = "blocked moving " + dir
		}

	case "explore":
		moved, revealed := g.exploreStep()
		g.spend(g.exploreCost, &reward)
		reward.InformationGain = float64(revealed)
		if moved {
			text = "explored new territory"
		} else {
			text = "nowhere new to explore"
		}

	case "collect":
		if g.resources[g.pos] {
			delete(g.resources, g.pos)
			g.energy = clampf(g.energy+g.collectGain, 0, g.maxEnergy)
			g.collectedThisEpisode++
			reward.TaskSuccess = 1
			reward.Homeostasis = 0.5
			text = "collected a resource"
		} else {
			reward.RiskCost += 0.1
			text = "no resource here"
		}

	case "rest":
		before := g.energy
		g.energy = clampf(g.energy+g.restGain, 0, g.maxEnergy)
		g.health = clampf(g.health+5, 0, g.maxHealth)
		reward.Homeostasis = (g.energy - before) / g.maxEnergy
		text = "rested and recovered energy"

	default:
		reward.RiskCost += 1
		text = "unknown action ignored"
	}

	// Homeostasis reward reflects how comfortable the body is (energy ratio).
	reward.Homeostasis += (g.energy/g.maxEnergy - 0.5)

	done, success, death := g.episodeStatus()
	info["episode_done"] = done
	info["episode_success"] = success
	info["death"] = death
	if success {
		reward.TaskSuccess += 1
	}
	if death {
		reward.RiskCost += 5
	}

	obs := g.observation()
	if done {
		g.resetEpisode(death)
	}

	return domain.Outcome{
		Success:     success,
		Observation: obs,
		Reward:      reward,
		Info:        info,
		Text:        text,
	}, nil
}

// spend consumes energy; when energy is exhausted the deficit damages health.
func (g *GridWorld) spend(cost float64, reward *domain.RewardVector) {
	reward.ResourceCost = cost / g.maxEnergy
	if g.energy >= cost {
		g.energy -= cost
		return
	}
	deficit := cost - g.energy
	g.energy = 0
	g.health = clampf(g.health-deficit, 0, g.maxHealth)
	reward.RiskCost += deficit / g.maxHealth
}

func (g *GridWorld) tryMove(dir string) (moved bool, revealed int) {
	dst := g.pos
	switch dir {
	case "up":
		dst.y--
	case "down":
		dst.y++
	case "left":
		dst.x--
	case "right":
		dst.x++
	default:
		return false, 0
	}
	if dst.x < 0 || dst.y < 0 || dst.x >= g.width || dst.y >= g.height || g.obstacles[dst] {
		return false, 0
	}
	g.pos = dst
	return true, g.revealAround(dst)
}

// exploreStep moves one step toward the nearest not-yet-explored, passable cell.
func (g *GridWorld) exploreStep() (moved bool, revealed int) {
	dirs := []struct {
		name string
		dx   int
		dy   int
	}{{"up", 0, -1}, {"down", 0, 1}, {"left", -1, 0}, {"right", 1, 0}}
	// Prefer a direction whose target cell is unexplored & passable.
	for _, d := range dirs {
		dst := cell{g.pos.x + d.dx, g.pos.y + d.dy}
		if dst.x < 0 || dst.y < 0 || dst.x >= g.width || dst.y >= g.height || g.obstacles[dst] {
			continue
		}
		if !g.explored[dst] {
			g.pos = dst
			return true, g.revealAround(dst)
		}
	}
	// Otherwise move to any passable neighbour.
	for _, d := range dirs {
		dst := cell{g.pos.x + d.dx, g.pos.y + d.dy}
		if dst.x < 0 || dst.y < 0 || dst.x >= g.width || dst.y >= g.height || g.obstacles[dst] {
			continue
		}
		g.pos = dst
		return true, g.revealAround(dst)
	}
	return false, 0
}

func (g *GridWorld) reveal(c cell) int {
	if c.x < 0 || c.y < 0 || c.x >= g.width || c.y >= g.height {
		return 0
	}
	if g.explored[c] {
		return 0
	}
	g.explored[c] = true
	return 1
}

func (g *GridWorld) revealAround(c cell) int {
	n := g.reveal(c)
	n += g.reveal(cell{c.x + 1, c.y})
	n += g.reveal(cell{c.x - 1, c.y})
	n += g.reveal(cell{c.x, c.y + 1})
	n += g.reveal(cell{c.x, c.y - 1})
	return n
}

func (g *GridWorld) exploredFraction() float64 {
	passable := g.width*g.height - len(g.obstacles)
	if passable <= 0 {
		return 1
	}
	return float64(len(g.explored)) / float64(passable)
}

// episodeStatus reports whether the current episode is over and how.
func (g *GridWorld) episodeStatus() (done, success, death bool) {
	death = g.health <= 0
	success = g.exploredFraction() >= g.exploreGoal
	elapsed := g.age - g.episodeStart
	timeout := elapsed >= g.episodeLength
	done = death || success || timeout
	return done, success, death
}

func (g *GridWorld) resetEpisode(death bool) {
	g.episodeStart = g.age
	g.collectedThisEpisode = 0
	g.explored = map[cell]bool{}
	g.pos = cell{0, 0}
	g.reveal(g.pos)
	if death {
		// Respawn body so the long-lived agent keeps learning across episodes.
		g.energy = g.maxEnergy
		g.health = g.maxHealth
	}
}

func (g *GridWorld) observation() domain.Observation {
	return domain.Observation{
		Tick: g.tick,
		Data: map[string]any{
			"position":          []int{g.pos.x, g.pos.y},
			"energy":            g.energy,
			"health":            g.health,
			"age":               float64(g.age),
			"explored_count":    len(g.explored),
			"explored_fraction": g.exploredFraction(),
			"on_resource":       g.resources[g.pos],
			"resources_left":    len(g.resources),
			"width":             g.width,
			"height":            g.height,
		},
		Text: "gridworld tick",
	}
}

func clampf(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
