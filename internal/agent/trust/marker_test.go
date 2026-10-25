package trust

import "testing"

func TestRemoveManagedEntryRemovesOnlyMarkedOnes(t *testing.T) {
	cfg := "" +
		"[other]\nx = 1\n" +
		"\n# " + Marker + "\n[projects.\"/repo/a\"]\ntrust_level = \"trusted\"\n" +
		"\n[projects.\"/repo/b\"]\ntrust_level = \"trusted\"\n"

	out, removed := removeManagedEntry(cfg, "/repo/a")
	if !removed {
		t.Fatal("a marked entry must be removable")
	}
	if contains(out, "/repo/a") {
		t.Error("the marked entry should be gone")
	}
	if !contains(out, "/repo/b") || !contains(out, "[other]") {
		t.Error("nothing else may be touched")
	}
	if contains(out, Marker) {
		t.Error("the marker comment must go with its entry")
	}
}

func TestRemoveManagedEntryLeavesUnmarkedEntries(t *testing.T) {
	cfg := "\n[projects.\"/repo/b\"]\ntrust_level = \"trusted\"\n"
	out, removed := removeManagedEntry(cfg, "/repo/b")
	if removed {
		t.Fatal("an unmarked entry is the user's, and must not be removed")
	}
	if out != cfg {
		t.Error("the config must be byte-identical when nothing is removed")
	}
}

func TestRemoveManagedEntryAbsentIsNotAnError(t *testing.T) {
	cfg := "[other]\nx = 1\n"
	out, removed := removeManagedEntry(cfg, "/repo/missing")
	if removed || out != cfg {
		t.Fatal("absent entry must be a no-op")
	}
}

func contains(hay, needle string) bool {
	return len(hay) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(hay); i++ {
			if hay[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
