package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func send(m model, msgs ...tea.Msg) model {
	for _, msg := range msgs {
		nm, _ := m.Update(msg)
		m = nm.(model)
	}
	return m
}

func keys(s string) tea.Msg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func TestFilterAndChoose(t *testing.T) {
	m := newModel(Options{Items: []Item{
		{Key: "acme-prod-use", Label: "use", Hint: "acme-prod-use"},
		{Key: "acme-prod-euw", Label: "euw", Hint: "acme-prod-euw"},
		{Key: "staging", Label: "staging", Hint: "staging"},
	}})
	m = send(m, keys("euw"))
	if len(m.matches) != 1 || m.items[m.matches[0].idx].Key != "acme-prod-euw" {
		t.Fatalf("filter 'euw' matched %d items", len(m.matches))
	}
	m = send(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.choice == nil || m.choice.Key != "acme-prod-euw" {
		t.Fatalf("choice = %+v", m.choice)
	}
}

func TestArrowsAndEsc(t *testing.T) {
	m := newModel(Options{Items: []Item{{Key: "a", Label: "a"}, {Key: "b", Label: "b"}, {Key: "c", Label: "c"}}, Selected: "b"})
	if m.cursor != 1 {
		t.Fatalf("Selected did not position the cursor: cursor=%d", m.cursor)
	}
	m = send(m, tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyUp})
	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want 1 (stops at the end of the list)", m.cursor)
	}
	m = send(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.choice != nil {
		t.Fatal("esc must not select")
	}
}

func TestPrefetchOnce(t *testing.T) {
	calls := 0
	m := newModel(Options{
		Items:       []Item{{Key: "a", Label: "a"}, {Key: "b", Label: "b"}},
		PreviewHead: func(Item) string { return "" },
		PreviewBody: func(Item) string { calls++; return "ok" },
		Prefetch:    true,
	})
	_ = m.wantPreviews()
	if len(m.pending) != 2 {
		t.Fatalf("pending = %d, want 2", len(m.pending))
	}
	if m.wantPreviews() != nil {
		t.Fatal("must not refetch what is already pending")
	}
}
