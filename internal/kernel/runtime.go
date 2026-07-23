// Package kernel is the resident runtime that drives the agent. The agent does
// not run itself: the kernel ticks it forward, enforces safety before execution,
// persists every important state change, and turns finished episodes into
// reflections and (evaluated) knowledge. It supports graceful shutdown, per-tick
// timeouts, single-step, a max-tick limit, pause/resume, and full recovery of
// identity/age/memory across restarts.
package kernel

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"liveagent/internal/config"
	"liveagent/internal/domain"
	"liveagent/internal/evolution"
	"liveagent/internal/memory"
	"liveagent/internal/reflection"
	"liveagent/internal/safety"
)

// Deps bundles everything the runtime orchestrates.
type Deps struct {
	Config    *config.Config
	Store     *memory.Store
	Env       domain.Environment
	Body      domain.Embodiment
	Agent     domain.Agent
	State     *domain.AgentState
	Guard     *safety.Guard
	Reflector *reflection.Reflector
	Evolution *evolution.Manager
	Logger    *slog.Logger
}

// Runtime is the main loop.
type Runtime struct {
	d       Deps
	weights config.RewardWeights

	episode *domain.Episode
	paused  atomic.Bool

	mu      sync.Mutex // guards persistence of shared State
	stopped bool
}

// New builds a Runtime and registers a fresh policy version if none is active.
func New(d Deps) *Runtime {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Runtime{d: d, weights: d.Config.Reward.Weights}
}

// Pause halts progression at the next tick boundary.
func (r *Runtime) Pause() { r.paused.Store(true) }

// Resume continues progression.
func (r *Runtime) Resume() { r.paused.Store(false) }

// Run drives ticks until ctx is cancelled, MaxTicks is reached, or (in step
// mode) a single tick completes. It returns nil on graceful shutdown.
func (r *Runtime) Run(ctx context.Context) error {
	rt := r.d.Config.Runtime
	r.d.Logger.Info("runtime.start",
		"agent_id", r.d.State.AgentID, "age", r.d.State.Age,
		"max_ticks", rt.MaxTicks, "step_mode", rt.StepMode)

	for {
		select {
		case <-ctx.Done():
			r.d.Logger.Info("runtime.shutdown", "reason", "context cancelled", "age", r.d.State.Age)
			return r.persist(context.Background())
		default:
		}

		if r.paused.Load() {
			time.Sleep(50 * time.Millisecond)
			continue
		}

		if rt.MaxTicks > 0 && r.d.State.Age >= rt.MaxTicks {
			r.d.Logger.Info("runtime.max_ticks_reached", "age", r.d.State.Age)
			return r.persist(context.Background())
		}

		if err := r.Step(ctx); err != nil {
			r.d.Logger.Error("runtime.tick_error", "err", err)
			// Persist and continue; a single tick error must not kill the agent.
			_ = r.persist(context.Background())
		}

		if rt.StepMode {
			return r.persist(context.Background())
		}
		if rt.TickInterval > 0 {
			select {
			case <-ctx.Done():
			case <-time.After(rt.TickInterval):
			}
		}
	}
}

// Step executes exactly one tick of the perceive-decide-act-learn loop.
func (r *Runtime) Step(parent context.Context) error {
	ctx := parent
	var cancel context.CancelFunc
	if to := r.d.Config.Runtime.TickTimeout; to > 0 {
		ctx, cancel = context.WithTimeout(parent, to)
		defer cancel()
	}

	st := r.d.State

	// 1. Proprioception -> body state.
	if body, err := r.d.Body.Proprioception(ctx); err == nil {
		st.BodyState = body
	}

	// 2. Observe the environment.
	obs, err := r.d.Env.Observe(ctx)
	if err != nil {
		return err
	}

	// 3. Update world/self model.
	if err := r.d.Agent.UpdateState(ctx, obs); err != nil {
		return err
	}

	// 4. Ensure an episode is open.
	if r.episode == nil {
		r.startEpisode(obs)
	}

	// 5. Select goal, plan, action.
	goal, err := r.d.Agent.SelectGoal(ctx)
	if err != nil {
		return err
	}
	r.episode.Goal = goal.Description
	plan, err := r.d.Agent.Plan(ctx, goal)
	if err != nil {
		return err
	}
	planNames := make([]string, 0, len(plan.Steps))
	for _, s := range plan.Steps {
		planNames = append(planNames, s.Name)
	}
	r.d.Logger.Info("plan", "goal", goal.ID, "steps", planNames, "rationale", plan.Rationale)
	action, err := r.d.Agent.SelectAction(ctx, plan)
	if err != nil {
		return err
	}

	// 6. Safety gate (defence in depth). A forbidden action is never executed,
	// regardless of expected reward.
	if dec := r.d.Guard.Check(action, st.BodyState); !dec.Allowed {
		r.d.Logger.Warn("safety.reject", "action", action.Name, "constraint", dec.Constraint, "reason", dec.Reason)
		_ = r.d.Store.RecordEvent(ctx, st.AgentID, obs.Tick, "safety_reject", map[string]any{
			"action": action.Name, "constraint": dec.Constraint, "reason": dec.Reason,
		})
		st.Age++
		return r.persist(ctx)
	}

	// 7. Execute.
	outcome, err := r.d.Env.Execute(ctx, action)
	if err != nil {
		return err
	}

	// 8. Refresh body after execution.
	if body, err := r.d.Body.Proprioception(ctx); err == nil {
		st.BodyState = body
	}

	// 9. Build & learn from the experience.
	scalar := r.weights.Scalarize(outcome.Reward)
	exp := domain.Experience{
		ID:          domain.NewID("exp"),
		AgentID:     st.AgentID,
		EpisodeID:   r.episode.ID,
		Tick:        outcome.Observation.Tick,
		Goal:        goal.Description,
		Observation: obs,
		Action:      action,
		Outcome:     outcome,
		Reward:      outcome.Reward,
		Tags:        goal.Tags,
		CreatedAt:   time.Now().UTC(),
	}
	if err := r.d.Agent.Learn(ctx, exp); err != nil {
		return err
	}
	r.episode.Trace = append(r.episode.Trace, exp)
	r.episode.TotalReward += scalar
	r.episode.EndTick = outcome.Observation.Tick

	st.Age++
	st.ResourceUsage.Ticks++

	r.d.Logger.Info("tick",
		"tick", outcome.Observation.Tick,
		"age", st.Age,
		"goal", goal.ID,
		"action", action.Name,
		"reward", round(scalar),
		"energy", body(st, "energy"),
		"health", body(st, "health"),
		"episode", r.episode.ID,
		"skill", plan.SkillName,
		"outcome", outcome.Text,
	)

	// 10. Episode boundary -> reflect & evolve.
	done, _ := outcome.Info["episode_done"].(bool)
	success, _ := outcome.Info["episode_success"].(bool)
	if done {
		if err := r.closeEpisode(ctx, success, st.BodyState); err != nil {
			r.d.Logger.Error("episode.close_error", "err", err)
		}
	}

	return r.persist(ctx)
}

func (r *Runtime) startEpisode(obs domain.Observation) {
	r.episode = &domain.Episode{
		ID:        domain.NewID("epi"),
		AgentID:   r.d.State.AgentID,
		StartTick: obs.Tick,
		CreatedAt: time.Now().UTC(),
	}
}

// closeEpisode persists the trajectory, reflects, and processes the reflection
// into (evaluated) principles/skills.
func (r *Runtime) closeEpisode(ctx context.Context, success bool, endBody map[string]any) error {
	ep := r.episode
	ep.Success = success
	ep.Summary = summarize(ep, success)
	ep.Tags = episodeTags(ep, success, endBody)

	if err := r.d.Store.SaveEpisode(ctx, *ep); err != nil {
		return err
	}

	ref, err := r.d.Reflector.Reflect(ctx, *ep, endBody)
	if err != nil {
		r.d.Logger.Warn("reflection.error", "err", err)
	}
	if !ref.Parsed {
		// Preserve raw output for audit, do not create principles.
		_ = r.d.Store.RecordEvent(ctx, ep.AgentID, ep.EndTick, "reflection_unparsed", map[string]any{"raw": ref.Raw})
	} else {
		res, err := r.d.Evolution.ProcessReflection(ctx, ep.AgentID, ref, success)
		if err != nil {
			return err
		}
		r.d.Logger.Info("episode.close",
			"episode", ep.ID, "success", success, "reward", round(ep.TotalReward),
			"principle", res.PrincipleID, "principle_status", res.PrincipleStatus,
			"skill", res.SkillID, "skill_status", res.SkillStatus,
		)
	}
	_ = r.d.Store.Consolidate(ctx)
	r.episode = nil
	return nil
}

// persist writes the current agent state (thread-safe).
func (r *Runtime) persist(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.d.Store.SaveAgentState(ctx, r.d.Config.Agent.Name, *r.d.State)
}

func body(st *domain.AgentState, key string) float64 {
	if st.BodyState == nil {
		return 0
	}
	if v, ok := st.BodyState[key].(float64); ok {
		return round(v)
	}
	return 0
}

func round(v float64) float64 { return float64(int(v*100)) / 100 }
