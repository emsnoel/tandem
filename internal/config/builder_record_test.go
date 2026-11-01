package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteBuilderAtomicAndShape(t *testing.T) {
	relay := filepath.Join(t.TempDir(), "relay")
	if err := WriteBuilder(relay, BuilderRecord{Kind: "codex", Autonomy: "auto", Adopted: true}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(relay, BuilderFile))
	if err != nil || string(b) != `{"kind":"codex","autonomy":"auto","adopted":true}`+"\n" {
		t.Fatalf("got %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(relay, BuilderFile+".tmp")); !os.IsNotExist(err) {
		t.Fatalf("the .tmp file is left behind: %v", err)
	}
}

func TestLoadBuilderReadsWhatWriteBuilderWrote(t *testing.T) {
	relay := t.TempDir()
	want := BuilderRecord{Kind: "codex", Autonomy: "auto", Adopted: true}
	if err := WriteBuilder(relay, want); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadBuilder(relay); err != nil || got != want {
		t.Fatalf("got %+v, %v", got, err)
	}
	if _, err := LoadBuilder(t.TempDir()); err == nil {
		t.Fatal("a missing record must be an error")
	}
}
