package telegram

import (
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"xray-checker/checker"
)

// Wave M9(3) follow-up: the RICH branch of menu:diag:details drops the cache
// staleness note that the plain branch appends («данные от HH:MM», produced
// by diagCacheAgeNote as «\n\n<i>данные от 15:04</i>»). The rich builders
// take the note as a param and render it as a trailing italic paragraph —
// per the RAW contract of this file, rich text entities carry PLAIN text
// (no HTML tags, no escaping — mirroring detailLines handling), so the <i>
// wrapper from the HTML note must be stripped before rendering.

// diagRichTextString extracts plain text from a rich-text tree (test-only
// helper; covers exactly the entity kinds the diagnostics builders emit).
func diagRichTextString(rt telego.RichText) string {
	switch v := rt.(type) {
	case *telego.RichTextPlain:
		return string(*v)
	case *telego.RichTextBold:
		return diagRichTextString(v.Text)
	case *telego.RichTextItalic:
		return diagRichTextString(v.Text)
	case *telego.RichTextList:
		parts := make([]string, 0, len(*v))
		for _, item := range *v {
			parts = append(parts, diagRichTextString(item))
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

// diagRichBlockString extracts the rendered text of one rich block.
func diagRichBlockString(block telego.InputRichBlock) string {
	switch v := block.(type) {
	case *telego.InputRichBlockParagraph:
		return diagRichTextString(v.Text)
	case *telego.InputRichBlockSectionHeading:
		return diagRichTextString(v.Text)
	case *telego.InputRichBlockPreformatted:
		return diagRichTextString(v.Text)
	case *telego.InputRichBlockDetails:
		return diagRichTextString(v.Summary)
	default:
		return ""
	}
}

// diagRichMessageText joins the rendered text of all blocks in the message.
func diagRichMessageText(msg *telego.InputRichMessage) string {
	if msg == nil {
		return ""
	}
	parts := make([]string, 0, len(msg.Blocks))
	for _, block := range msg.Blocks {
		parts = append(parts, diagRichBlockString(block))
	}
	return strings.Join(parts, "\n")
}

func noteTestReports() []checker.ProxyDiagReport {
	return []checker.ProxyDiagReport{
		{ProxyName: "P1", Protocol: "vless", Status: "online", Targets: []checker.TargetDiagResult{{Success: true, Latency: 30 * time.Millisecond}}},
		{ProxyName: "P2", Protocol: "vless", Status: "online", Targets: []checker.TargetDiagResult{{Success: true, Latency: 45 * time.Millisecond}}},
	}
}

func TestRichDetailsBuilderWithNoteCarriesCacheAgeLine(t *testing.T) {
	b := &Bot{}
	reports := noteTestReports()

	// The exact form diagCacheAgeNote() produces for the HTML paths:
	// blank-line padding + <i> wrapper. The rich builder must strip both.
	note := "\n\n<i>данные от 14:05</i>"

	msg, totalPages := b.buildDiagnosticsDetailsRichMessageWithTargetNote(reports, "local", 1, note)
	if msg == nil {
		t.Fatalf("expected non-nil rich message")
	}
	if totalPages != 1 {
		t.Errorf("expected 1 total page, got %d", totalPages)
	}

	text := diagRichMessageText(msg)
	if !strings.Contains(text, "данные от 14:05") {
		t.Fatalf("rich details message must carry the cache-age line, got: %q", text)
	}
	if strings.Contains(text, "<i>") || strings.Contains(text, "</i>") {
		t.Fatalf("rich entities are RAW plain text: HTML <i> wrapper must be stripped, got: %q", text)
	}

	// The note renders as the trailing italic paragraph.
	last, ok := msg.Blocks[len(msg.Blocks)-1].(*telego.InputRichBlockParagraph)
	if !ok {
		t.Fatalf("last block must be the note paragraph, got %T", msg.Blocks[len(msg.Blocks)-1])
	}
	it, ok := last.Text.(*telego.RichTextItalic)
	if !ok {
		t.Fatalf("note text must be italic, got %T", last.Text)
	}
	plain, ok := it.Text.(*telego.RichTextPlain)
	if !ok {
		t.Fatalf("note inner text must be plain, got %T", it.Text)
	}
	if got := string(*plain); got != "данные от 14:05" {
		t.Errorf("note text = %q, want %q", got, "данные от 14:05")
	}
}

func TestRichDetailsBuilderWithNoteBlockCountParity(t *testing.T) {
	b := &Bot{}
	reports := noteTestReports()

	base, _ := b.buildDiagnosticsDetailsRichMessageWithTargetNote(reports, "local", 1, "")
	withNote, _ := b.buildDiagnosticsDetailsRichMessageWithTargetNote(reports, "local", 1, "\n\n<i>данные от 14:05</i>")

	if len(withNote.Blocks) != len(base.Blocks)+1 {
		t.Errorf("note must add exactly one trailing block: base=%d, withNote=%d", len(base.Blocks), len(withNote.Blocks))
	}

	// Plain-form note input is accepted too (RAW contract): same result.
	plain, _ := b.buildDiagnosticsDetailsRichMessageWithTargetNote(reports, "local", 1, "данные от 14:05")
	if len(plain.Blocks) != len(withNote.Blocks) {
		t.Errorf("plain-form note must render identically: plain=%d, html-form=%d", len(plain.Blocks), len(withNote.Blocks))
	}
}

func TestRichDetailsBuilderWrapperStaysNoteFree(t *testing.T) {
	b := &Bot{}
	reports := noteTestReports()

	// Old wrapper passes "" — cached renders wired through it stay note-free.
	msg, totalPages := b.buildDiagnosticsDetailsRichMessageWithTarget(reports, "local", 1)
	if msg == nil {
		t.Fatalf("expected non-nil rich message from the old wrapper")
	}
	if totalPages != 1 {
		t.Errorf("expected 1 total page, got %d", totalPages)
	}
	if text := diagRichMessageText(msg); strings.Contains(text, "данные от") {
		t.Errorf("old wrapper must not render a note, got: %q", text)
	}
}

func TestRichSummaryBuilderWithNoteCarriesCacheAgeLine(t *testing.T) {
	b := &Bot{}
	reports := noteTestReports()

	msg := b.buildDiagnosticsRichMessageWithTargetNote(reports, "local", b.getMasterASN(), "\n\n<i>данные от 14:05</i>")
	if msg == nil {
		t.Fatalf("expected non-nil rich message")
	}
	text := diagRichMessageText(msg)
	if !strings.Contains(text, "данные от 14:05") {
		t.Fatalf("rich summary message must carry the cache-age line, got: %q", text)
	}
	if strings.Contains(text, "<i>") || strings.Contains(text, "</i>") {
		t.Fatalf("rich entities are RAW plain text: HTML <i> wrapper must be stripped, got: %q", text)
	}

	last, ok := msg.Blocks[len(msg.Blocks)-1].(*telego.InputRichBlockParagraph)
	if !ok {
		t.Fatalf("last block must be the note paragraph, got %T", msg.Blocks[len(msg.Blocks)-1])
	}
	if it, ok := last.Text.(*telego.RichTextItalic); ok {
		if plain, ok := it.Text.(*telego.RichTextPlain); ok && string(*plain) != "данные от 14:05" {
			t.Errorf("note text = %q, want %q", string(*plain), "данные от 14:05")
		}
	} else {
		t.Fatalf("note text must be italic, got %T", last.Text)
	}

	// Old summary wrapper stays note-free.
	old := b.buildDiagnosticsRichMessageWithTarget(reports, "local", b.getMasterASN())
	if old == nil {
		t.Fatalf("expected non-nil rich message from the old summary wrapper")
	}
	if text := diagRichMessageText(old); strings.Contains(text, "данные от") {
		t.Errorf("old summary wrapper must not render a note, got: %q", text)
	}
}
