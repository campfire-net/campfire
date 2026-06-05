package fs

// Tests for ListMessagesPage — the paged read primitive behind bounded/chunked
// sync (campfireagent-6d3).

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/message"
)

func TestListMessagesPage_PagingAndMore(t *testing.T) {
	tr := newTestTransport(t)
	cf := newTestCampfire(t)
	if err := tr.Init(cf); err != nil {
		t.Fatalf("Init() error: %v", err)
	}
	cfID := cf.PublicKeyHex()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)

	var ids []string
	for i := 0; i < 5; i++ {
		msg, err := message.NewMessage(message.MustNewEd25519Signer(priv, pub), []byte("x"), nil, nil)
		if err != nil {
			t.Fatalf("NewMessage(): %v", err)
		}
		if err := tr.WriteMessage(cfID, msg); err != nil {
			t.Fatalf("WriteMessage(): %v", err)
		}
		ids = append(ids, msg.ID)
		time.Sleep(time.Millisecond) // unique nanos prefix
	}

	// Page 1: limit 2 from the start.
	p1, err := tr.ListMessagesPage(cfID, "", 2)
	if err != nil {
		t.Fatalf("ListMessagesPage(\"\", 2): %v", err)
	}
	if len(p1.Messages) != 2 || p1.Messages[0].Message.ID != ids[0] || p1.Messages[1].Message.ID != ids[1] {
		t.Fatalf("page 1 wrong: got %d messages", len(p1.Messages))
	}
	if !p1.More {
		t.Fatal("page 1: More = false, want true (3 messages remain)")
	}
	if p1.LastListed != p1.Messages[1].Leaf {
		t.Fatalf("page 1 LastListed %q != last message leaf %q", p1.LastListed, p1.Messages[1].Leaf)
	}

	// Page 2: continue from page 1's LastListed.
	p2, err := tr.ListMessagesPage(cfID, p1.LastListed, 2)
	if err != nil {
		t.Fatalf("ListMessagesPage(page2): %v", err)
	}
	if len(p2.Messages) != 2 || p2.Messages[0].Message.ID != ids[2] || p2.Messages[1].Message.ID != ids[3] {
		t.Fatalf("page 2 wrong")
	}
	if !p2.More {
		t.Fatal("page 2: More = false, want true (1 message remains)")
	}

	// Page 3: final page, not full → More = false.
	p3, err := tr.ListMessagesPage(cfID, p2.LastListed, 2)
	if err != nil {
		t.Fatalf("ListMessagesPage(page3): %v", err)
	}
	if len(p3.Messages) != 1 || p3.Messages[0].Message.ID != ids[4] {
		t.Fatalf("page 3 wrong")
	}
	if p3.More {
		t.Fatal("page 3: More = true, want false (end of history)")
	}

	// Past the end: empty page, no More, empty LastListed.
	p4, err := tr.ListMessagesPage(cfID, p3.LastListed, 2)
	if err != nil {
		t.Fatalf("ListMessagesPage(page4): %v", err)
	}
	if len(p4.Messages) != 0 || p4.More || p4.LastListed != "" {
		t.Fatalf("past-end page wrong: %d messages, More=%v, LastListed=%q", len(p4.Messages), p4.More, p4.LastListed)
	}

	// limit <= 0: full read, one page, no More — ListMessagesSince equivalence.
	pAll, err := tr.ListMessagesPage(cfID, "", 0)
	if err != nil {
		t.Fatalf("ListMessagesPage(no limit): %v", err)
	}
	if len(pAll.Messages) != 5 || pAll.More {
		t.Fatalf("unlimited page wrong: %d messages, More=%v", len(pAll.Messages), pAll.More)
	}
}

// TestListMessagesPage_CorruptTrailingFile verifies the LastListed contract:
// when files at the end of a page fail to decode, LastListed still reflects
// the last directory entry examined, so a paging caller's cursor advances past
// the corrupt file instead of stalling on it forever.
func TestListMessagesPage_CorruptTrailingFile(t *testing.T) {
	tr := newTestTransport(t)
	cf := newTestCampfire(t)
	if err := tr.Init(cf); err != nil {
		t.Fatalf("Init() error: %v", err)
	}
	cfID := cf.PublicKeyHex()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)

	msg, err := message.NewMessage(message.MustNewEd25519Signer(priv, pub), []byte("x"), nil, nil)
	if err != nil {
		t.Fatalf("NewMessage(): %v", err)
	}
	if err := tr.WriteMessage(cfID, msg); err != nil {
		t.Fatalf("WriteMessage(): %v", err)
	}
	time.Sleep(time.Millisecond)

	// Plant a corrupt .cbor that sorts AFTER the real message (later nanos).
	full, err := tr.ListMessagesPage(cfID, "", 0)
	if err != nil || len(full.Messages) != 1 {
		t.Fatalf("setup read: %v (%d messages)", err, len(full.Messages))
	}
	realLeaf := full.Messages[0].Leaf
	corruptLeaf := "9" + realLeaf[1:] // same width, sorts after any real leaf
	// The bucketed layout dir of the real message:
	var corruptDir string
	dir := filepath.Join(tr.CampfireDir(cfID), "messages")
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() {
			dd, _ := os.ReadDir(filepath.Join(dir, e.Name()))
			for _, d := range dd {
				corruptDir = filepath.Join(dir, e.Name(), d.Name())
			}
		}
	}
	if corruptDir == "" {
		t.Fatal("could not locate bucketed message dir")
	}
	if err := os.WriteFile(filepath.Join(corruptDir, corruptLeaf), []byte("not cbor"), 0600); err != nil {
		t.Fatalf("planting corrupt file: %v", err)
	}

	page, err := tr.ListMessagesPage(cfID, "", 10)
	if err != nil {
		t.Fatalf("ListMessagesPage(): %v", err)
	}
	if len(page.Messages) != 1 {
		t.Fatalf("got %d decoded messages, want 1 (corrupt file skipped)", len(page.Messages))
	}
	if page.LastListed != corruptLeaf {
		t.Fatalf("LastListed = %q, want the corrupt leaf %q (cursor must advance past undecodable files)", page.LastListed, corruptLeaf)
	}
	if page.More {
		t.Fatal("More = true, want false")
	}
}
