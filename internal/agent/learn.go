package agent

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"strings"

	"liveagent/internal/domain"
	"liveagent/internal/llm"
	"liveagent/internal/store"
)

// The verified-learning loop. Learning is gated on VERIFICATION: only runs whose
// success was independently confirmed produce lessons, so the memory can only
// ever be seeded by grounded outcomes — never by a self-reported (possibly
// false) success. Lessons are distilled into short, transferable notes and
// recalled into later, lexically-similar tasks.

// semanticMinCosine is the minimum cosine similarity for an embedding-based
// match to be considered relevant. lexicalMinOverlap plays the same role for
// the word-overlap fallback.
const semanticMinCosine = 0.55

// recallLessons loads stored notes and returns the most relevant lesson texts
// for the current task, ranked by relevance BLENDED with a quality prior (how
// often the lesson previously contributed to a verified win). Relevance is
// semantic (embeddings) when an embedder is configured — robust to paraphrase
// and cross-language tasks — and degrades to word overlap otherwise. It records
// which lesson ids were surfaced so the run can credit them on a verified
// success.
func (a *Agent) recallLessons(ctx context.Context) []string {
	a.lessonIDs = nil
	if a.store == nil || !a.cfg.Learn.On() {
		return nil
	}
	notes, err := a.store.ListNotes(ctx, a.cfg.Agent.Name, 200)
	if err != nil || len(notes) == 0 {
		return nil
	}

	rel, mode := a.relevance(ctx, notes)

	type scored struct {
		id    int64
		text  string
		rel   float64
		score float64
	}
	ranked := make([]scored, 0, len(notes))
	for _, n := range notes {
		r, ok := rel[n.ID]
		if !ok {
			continue
		}
		// Relevance dominates; a proven lesson gets a small, bounded boost so that
		// among comparably-relevant lessons the trusted one surfaces first.
		boost := n.Wins
		if boost > 5 {
			boost = 5
		}
		ranked = append(ranked, scored{id: n.ID, text: strings.TrimSpace(n.Text), rel: r, score: r + 0.03*float64(boost)})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].rel > ranked[j].rel
	})

	k := a.cfg.Learn.MaxInject
	if k <= 0 {
		k = 3
	}
	out := make([]string, 0, k)
	seen := map[string]bool{}
	for _, r := range ranked {
		if len(out) >= k {
			break
		}
		if r.text == "" || seen[r.text] {
			continue
		}
		seen[r.text] = true
		out = append(out, r.text)
		a.lessonIDs = append(a.lessonIDs, r.id)
	}
	if len(a.lessonIDs) > 0 {
		_ = a.store.MarkNotesUsed(ctx, a.lessonIDs)
		a.logger.Info("learn.recall", "mode", mode, "candidates", len(ranked), "injected", len(a.lessonIDs))
	}
	return out
}

// relevance scores each note against the current task, returning only notes
// above the relevance threshold and the mode used ("semantic" or "lexical").
func (a *Agent) relevance(ctx context.Context, notes []store.Note) (map[int64]float64, string) {
	if sem, ok := a.semanticRelevance(ctx, notes); ok {
		return sem, "semantic"
	}
	taskTokens := tokenSet(a.task)
	out := map[int64]float64{}
	for _, n := range notes {
		if r := jaccard(taskTokens, tokenSet(n.Key+" "+n.Text)); r > 0 {
			out[n.ID] = r
		}
	}
	return out, "lexical"
}

// semanticRelevance embeds the task and the notes (backfilling any missing note
// vectors, which also self-heals older lessons) and scores by cosine similarity.
// Returns ok=false if no embedder is configured or any embedding step fails, so
// the caller can fall back to lexical matching.
func (a *Agent) semanticRelevance(ctx context.Context, notes []store.Note) (map[int64]float64, bool) {
	if a.embedder == nil {
		return nil, false
	}
	vecs, err := a.store.NoteEmbeddings(ctx, a.cfg.Agent.Name)
	if err != nil {
		return nil, false
	}
	// Backfill notes missing an embedding in one batch call.
	var missing []store.Note
	for _, n := range notes {
		if _, ok := vecs[n.ID]; !ok {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		texts := make([]string, len(missing))
		for i, n := range missing {
			texts[i] = n.Key + "\n" + n.Text
		}
		embs, err := a.embedder.Embed(ctx, texts)
		if err != nil || len(embs) != len(missing) {
			return nil, false
		}
		for i, n := range missing {
			vecs[n.ID] = embs[i]
			_ = a.store.SetNoteEmbedding(ctx, n.ID, embs[i])
		}
	}
	taskEmb, err := a.embedder.Embed(ctx, []string{a.task})
	if err != nil || len(taskEmb) != 1 {
		return nil, false
	}
	tv := taskEmb[0]
	out := map[int64]float64{}
	for _, n := range notes {
		v, ok := vecs[n.ID]
		if !ok {
			continue
		}
		if c := cosine(tv, v); c >= semanticMinCosine {
			out[n.ID] = c
		}
	}
	return out, true
}

// creditLessons rewards the recalled lessons that were in play during a verified
// success, strengthening their quality prior for future recall.
func (a *Agent) creditLessons(ctx context.Context) {
	if a.store == nil || len(a.lessonIDs) == 0 {
		return
	}
	_ = a.store.MarkNotesWin(ctx, a.lessonIDs)
}

// evictWeakLessons prunes lessons that keep getting surfaced but never help.
func (a *Agent) evictWeakLessons(ctx context.Context) {
	if a.store == nil || a.cfg.Learn.EvictMinUses <= 0 {
		return
	}
	if n, err := a.store.EvictWeak(ctx, a.cfg.Agent.Name, a.cfg.Learn.EvictMinUses); err == nil && n > 0 {
		a.logger.Info("learn.evicted", "count", n)
	}
}

// distillLesson runs after a VERIFIED-successful finish. It asks the model to
// extract one short, reusable lesson and persists it as a note, skipping
// near-duplicates of what is already stored.
func (a *Agent) distillLesson(ctx context.Context, summary string) {
	if a.store == nil || !a.cfg.Learn.On() {
		return
	}
	system := "You distill ONE reusable lesson from a VERIFIED-successful agent run so future similar tasks go " +
		"faster. Focus on transferable know-how: the approach that worked, the key obstacle and how it was " +
		"overcome, and pitfalls to avoid. Omit task-specific IDs/URLs unless essential. Respond with ONLY JSON: " +
		`{"key": "<short gist, <=12 words>", "lesson": "<1-4 concrete sentences>"}. If nothing is generally ` +
		`reusable, return {"key":"","lesson":""}.`
	user := "TASK:\n" + a.task + "\n\nFINISH SUMMARY:\n" + summary +
		"\n\nWHAT WAS TRIED (transcript tail):\n" + a.tx.tail(5000)

	resp, err := a.llm.Generate(ctx, domain.LLMRequest{
		Messages:    []domain.Message{{Role: "system", Content: system}, {Role: "user", Content: user}},
		Temperature: 0.2,
		MaxTokens:   400,
	})
	if err != nil {
		a.logger.Warn("learn.distill_failed", "err", err.Error())
		return
	}
	obj, err := llm.ExtractJSONObject(resp.Text)
	if err != nil {
		return
	}
	var out struct {
		Key    string `json:"key"`
		Lesson string `json:"lesson"`
	}
	if json.Unmarshal([]byte(obj), &out) != nil {
		return
	}
	out.Key = strings.TrimSpace(out.Key)
	out.Lesson = strings.TrimSpace(out.Lesson)
	if out.Lesson == "" {
		return
	}
	// Embed the candidate once (if possible); reused for both semantic
	// de-duplication and, on save, for storage so future recall is semantic.
	var candVec []float32
	if a.embedder != nil {
		if embs, e := a.embedder.Embed(ctx, []string{out.Key + "\n" + out.Lesson}); e == nil && len(embs) == 1 {
			candVec = embs[0]
		}
	}
	// Near-duplicate of an existing lesson? Treat the independent re-derivation as
	// a CONFIRMATION and reinforce that lesson instead of cluttering the store.
	if id, ok := a.duplicateLesson(ctx, out.Key, out.Lesson, candVec); ok {
		_ = a.store.ReinforceNote(ctx, id)
		a.logger.Info("learn.reinforced", "id", id, "key", out.Key)
		return
	}
	id, err := a.store.SaveNote(ctx, a.cfg.Agent.Name, out.Key, out.Lesson)
	if err != nil {
		a.logger.Warn("learn.save_failed", "err", err.Error())
		return
	}
	if candVec != nil {
		_ = a.store.SetNoteEmbedding(ctx, id, candVec)
	}
	a.logger.Info("learn.saved", "id", id, "key", out.Key)
}

// semanticDupCosine is the cosine threshold above which two lessons are treated
// as semantic duplicates.
const semanticDupCosine = 0.92

// duplicateLesson finds an existing lesson that is a near-duplicate of the given
// one and returns its id. It matches on identical key, high semantic similarity
// (when an embedding is available), or — as a fallback — high word overlap.
func (a *Agent) duplicateLesson(ctx context.Context, key, lesson string, candVec []float32) (int64, bool) {
	notes, err := a.store.ListNotes(ctx, a.cfg.Agent.Name, 200)
	if err != nil {
		return 0, false
	}
	var vecs map[int64][]float32
	if candVec != nil {
		vecs, _ = a.store.NoteEmbeddings(ctx, a.cfg.Agent.Name)
	}
	lt := tokenSet(lesson)
	for _, n := range notes {
		if key != "" && strings.EqualFold(strings.TrimSpace(n.Key), key) {
			return n.ID, true
		}
		if vecs != nil {
			if v, ok := vecs[n.ID]; ok && cosine(candVec, v) >= semanticDupCosine {
				return n.ID, true
			}
		}
		if similar(lt, tokenSet(n.Text)) {
			return n.ID, true
		}
	}
	return 0, false
}

// --- lightweight lexical helpers ---

var stopwords = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "or": true, "of": true, "to": true, "in": true,
	"on": true, "for": true, "with": true, "as": true, "is": true, "are": true, "be": true, "it": true,
	"this": true, "that": true, "each": true, "them": true, "into": true, "from": true, "by": true,
	"at": true, "so": true, "if": true, "when": true, "only": true, "them.": true, "you": true, "your": true,
}

func tokenSet(s string) map[string]bool {
	set := map[string]bool{}
	for _, f := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	}) {
		if len(f) < 3 || stopwords[f] {
			continue
		}
		set[f] = true
	}
	return set
}

func overlap(a, b map[string]bool) int {
	n := 0
	small, large := a, b
	if len(b) < len(a) {
		small, large = b, a
	}
	for t := range small {
		if large[t] {
			n++
		}
	}
	return n
}

// jaccard returns |A∩B| / |A∪B| in [0,1].
func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := overlap(a, b)
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// cosine returns the cosine similarity of two equal-length vectors.
func cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// similar reports high Jaccard-ish overlap, used for de-duplicating lessons.
func similar(a, b map[string]bool) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	inter := overlap(a, b)
	union := len(a) + len(b) - inter
	if union == 0 {
		return false
	}
	return inter*100/union >= 55
}
