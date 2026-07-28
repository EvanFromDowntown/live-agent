package agent

import (
	"context"
	"strings"
	"testing"

	"liveagent/internal/domain"
)

// learnStub returns a fixed text for the (tool-less) distill call.
type learnStub struct{ text string }

func (s *learnStub) Generate(_ context.Context, _ domain.LLMRequest) (domain.LLMResponse, error) {
	return domain.LLMResponse{Text: s.text}, nil
}

func TestDistillSavesThenReinforces(t *testing.T) {
	stub := &learnStub{text: `{"key":"zhihu crawl","lesson":"Visit the Zhihu homepage first to obtain session cookies, then navigate with a headless browser to bypass the 403 anti-bot gate."}`}
	ag := newTestAgent(t, stub)
	ag.task = "crawl a zhihu question and save answers"
	ag.tx = newTranscript(0, 0, 0)

	ctx := context.Background()
	ag.distillLesson(ctx, "did it")
	notes, _ := ag.store.ListNotes(ctx, ag.cfg.Agent.Name, 10)
	if len(notes) != 1 || !strings.Contains(notes[0].Text, "session cookies") {
		t.Fatalf("expected 1 saved lesson, got %+v", notes)
	}

	// Re-deriving the same lesson must REINFORCE (wins++), not duplicate.
	ag.distillLesson(ctx, "did it again")
	notes, _ = ag.store.ListNotes(ctx, ag.cfg.Agent.Name, 10)
	if len(notes) != 1 {
		t.Fatalf("expected dedup to keep 1 note, got %d", len(notes))
	}
	if notes[0].Wins < 1 {
		t.Fatalf("expected reinforcement to bump wins, got %+v", notes[0])
	}
}

func TestDistillIgnoresEmpty(t *testing.T) {
	ag := newTestAgent(t, &learnStub{text: `{"key":"","lesson":""}`})
	ag.task = "t"
	ag.tx = newTranscript(0, 0, 0)
	ag.distillLesson(context.Background(), "s")
	notes, _ := ag.store.ListNotes(context.Background(), ag.cfg.Agent.Name, 10)
	if len(notes) != 0 {
		t.Fatalf("expected no lesson for empty distill, got %d", len(notes))
	}
}

func TestRecallRanksByOverlap(t *testing.T) {
	ag := newTestAgent(t, &scriptLLM{})
	ctx := context.Background()
	name := ag.cfg.Agent.Name
	_, _ = ag.store.SaveNote(ctx, name, "zhihu cookies", "Visit zhihu homepage first to obtain cookies then navigate playwright to bypass 403")
	_, _ = ag.store.SaveNote(ctx, name, "pdf tables", "Use pdfplumber to extract tables from invoice documents")

	ag.task = "crawl a zhihu question page and save the answers"
	got := ag.recallLessons(ctx)
	if len(got) == 0 || !strings.Contains(got[0], "zhihu") {
		t.Fatalf("expected zhihu lesson recalled first, got %+v", got)
	}
	for _, g := range got {
		if strings.Contains(g, "pdfplumber") {
			t.Fatalf("unrelated pdf lesson must not be recalled: %+v", got)
		}
	}
}

func TestRecallRespectsMaxInject(t *testing.T) {
	ag := newTestAgent(t, &scriptLLM{})
	ctx := context.Background()
	name := ag.cfg.Agent.Name
	for _, s := range []string{
		"crawl zhihu answers alpha technique",
		"crawl zhihu answers beta technique",
		"crawl zhihu answers gamma technique",
		"crawl zhihu answers delta technique",
	} {
		_, _ = ag.store.SaveNote(ctx, name, "k", s)
	}
	ag.cfg.Learn.MaxInject = 2
	ag.task = "crawl zhihu answers"
	if got := ag.recallLessons(ctx); len(got) != 2 {
		t.Fatalf("expected max_inject=2 lessons, got %d", len(got))
	}
}

func TestRecallMarksUsedAndCreditsWins(t *testing.T) {
	ag := newTestAgent(t, &scriptLLM{})
	ctx := context.Background()
	name := ag.cfg.Agent.Name
	_, _ = ag.store.SaveNote(ctx, name, "k", "crawl zhihu answers useful technique")

	ag.task = "crawl zhihu answers"
	if got := ag.recallLessons(ctx); len(got) != 1 || len(ag.lessonIDs) != 1 {
		t.Fatalf("expected 1 recalled lesson + id, got texts=%v ids=%v", got, ag.lessonIDs)
	}
	notes, _ := ag.store.ListNotes(ctx, name, 10)
	if notes[0].Uses != 1 {
		t.Fatalf("expected uses=1 after recall, got %+v", notes[0])
	}
	ag.creditLessons(ctx)
	notes, _ = ag.store.ListNotes(ctx, name, 10)
	if notes[0].Wins != 1 {
		t.Fatalf("expected wins=1 after credit, got %+v", notes[0])
	}
}

func TestRecallPrefersProvenLesson(t *testing.T) {
	ag := newTestAgent(t, &scriptLLM{})
	ctx := context.Background()
	name := ag.cfg.Agent.Name
	_, _ = ag.store.SaveNote(ctx, name, "k", "crawl zhihu answers using approach unproven")
	provenID, _ := ag.store.SaveNote(ctx, name, "k", "crawl zhihu answers using approach proven")
	for i := 0; i < 3; i++ {
		_ = ag.store.MarkNotesWin(ctx, []int64{provenID})
	}
	ag.task = "crawl zhihu answers"
	got := ag.recallLessons(ctx)
	if len(got) == 0 || !strings.Contains(got[0], "proven") || strings.Contains(got[0], "unproven") {
		t.Fatalf("expected proven lesson ranked first, got %+v", got)
	}
}

func TestEvictWeakLessons(t *testing.T) {
	ag := newTestAgent(t, &scriptLLM{})
	ctx := context.Background()
	name := ag.cfg.Agent.Name
	weakID, _ := ag.store.SaveNote(ctx, name, "weak", "never helped lesson")
	strongID, _ := ag.store.SaveNote(ctx, name, "strong", "helped lesson")
	for i := 0; i < 8; i++ {
		_ = ag.store.MarkNotesUsed(ctx, []int64{weakID, strongID})
	}
	_ = ag.store.MarkNotesWin(ctx, []int64{strongID}) // strong has a win → survives

	ag.cfg.Learn.EvictMinUses = 8
	ag.evictWeakLessons(ctx)

	notes, _ := ag.store.ListNotes(ctx, name, 10)
	if len(notes) != 1 || notes[0].ID != strongID {
		t.Fatalf("expected only the strong lesson to survive, got %+v", notes)
	}
}

// conceptEmbedder maps text to a concept-presence vector. Each dimension is a
// set of synonyms (across languages); the vector has 1.0 where any synonym
// appears. This lets tests exercise cross-language / paraphrase semantic recall
// deterministically without a real embedding endpoint.
type conceptEmbedder struct{ dims [][]string }

func (c *conceptEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		lt := strings.ToLower(t)
		v := make([]float32, len(c.dims))
		for d, kws := range c.dims {
			for _, k := range kws {
				if strings.Contains(lt, strings.ToLower(k)) {
					v[d] = 1
					break
				}
			}
		}
		out[i] = v
	}
	return out, nil
}

func TestSemanticRecallCrossLanguage(t *testing.T) {
	ag := newTestAgent(t, &scriptLLM{})
	ag.embedder = &conceptEmbedder{dims: [][]string{
		{"crawl", "抓取", "爬取"},
		{"zhihu", "知乎"},
		{"pdf", "invoice"},
	}}
	ctx := context.Background()
	name := ag.cfg.Agent.Name
	_, _ = ag.store.SaveNote(ctx, name, "zhihu", "Crawl zhihu: visit homepage first for cookies then use a headless browser")
	_, _ = ag.store.SaveNote(ctx, name, "pdf", "Use pdfplumber to extract tables from an invoice pdf")

	// Chinese task: lexical token set is empty, so only semantic recall can work.
	ag.task = "抓取知乎问题页面的所有回答"
	if len(tokenSet(ag.task)) != 0 {
		t.Fatalf("precondition: expected empty lexical token set for CJK task")
	}
	got := ag.recallLessons(ctx)
	if len(got) == 0 || !strings.Contains(got[0], "zhihu") {
		t.Fatalf("expected cross-language semantic recall of zhihu lesson, got %+v", got)
	}
	for _, g := range got {
		if strings.Contains(g, "pdfplumber") {
			t.Fatalf("unrelated pdf lesson must not be recalled: %+v", got)
		}
	}
}

func TestSemanticBackfillStoresVectors(t *testing.T) {
	ag := newTestAgent(t, &scriptLLM{})
	ag.embedder = &conceptEmbedder{dims: [][]string{{"crawl"}, {"zhihu"}}}
	ctx := context.Background()
	name := ag.cfg.Agent.Name
	_, _ = ag.store.SaveNote(ctx, name, "k", "crawl zhihu answers") // saved WITHOUT vector

	if vecs, _ := ag.store.NoteEmbeddings(ctx, name); len(vecs) != 0 {
		t.Fatalf("expected no stored vectors before recall, got %d", len(vecs))
	}
	ag.task = "crawl zhihu"
	_ = ag.recallLessons(ctx)
	if vecs, _ := ag.store.NoteEmbeddings(ctx, name); len(vecs) != 1 {
		t.Fatalf("expected recall to backfill 1 vector, got %d", len(vecs))
	}
}

func TestSemanticDedupReinforces(t *testing.T) {
	stub := &learnStub{text: `{"key":"a","lesson":"Crawl zhihu by visiting the homepage first to obtain cookies"}`}
	ag := newTestAgent(t, stub)
	ag.embedder = &conceptEmbedder{dims: [][]string{{"crawl", "scrape"}, {"zhihu"}, {"cookies", "session"}}}
	ag.task = "t"
	ag.tx = newTranscript(0, 0, 0)
	ctx := context.Background()

	ag.distillLesson(ctx, "s") // saves note #1 (+ stores its vector)

	// Different wording, SAME concepts → lexical overlap is low but semantic
	// similarity is high, so it must reinforce rather than create a duplicate.
	stub.text = `{"key":"b","lesson":"To scrape zhihu, first grab session cookies from the landing page"}`
	ag.distillLesson(ctx, "s")

	notes, _ := ag.store.ListNotes(ctx, ag.cfg.Agent.Name, 10)
	if len(notes) != 1 {
		t.Fatalf("expected semantic dedup to keep 1 note, got %d: %+v", len(notes), notes)
	}
	if notes[0].Wins < 1 {
		t.Fatalf("expected reinforcement to bump wins, got %+v", notes[0])
	}
}

func TestLessonsInjectedIntoAsk(t *testing.T) {
	ag := newTestAgent(t, &scriptLLM{})
	ag.task = "x"
	ag.env = map[string]any{"os": "test"}
	ag.lessons = []string{"Always visit homepage first for cookies."}
	ask := ag.buildAsk()
	if !strings.Contains(ask, "lessons_from_past_runs") || !strings.Contains(ask, "homepage first") {
		t.Fatalf("expected recalled lessons injected into ask, got:\n%s", ask)
	}
}
