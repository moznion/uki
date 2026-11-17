package record

import "testing"

func TestValidSHA(t *testing.T) {
	ok := []string{
		"712eb857f9862490b5c79d40fbdd7f1c8767be51",
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	ng := []string{"", "a", "HEAD", "712eb857f986", "712EB857F9862490B5C79D40FBDD7F1C8767BE51", "../../../../../etc/passwd/xxxxxxxxxxxxxxx"}
	for _, s := range ok {
		if !ValidSHA(s) {
			t.Errorf("ValidSHA(%q) = false", s)
		}
	}
	for _, s := range ng {
		if ValidSHA(s) {
			t.Errorf("ValidSHA(%q) = true", s)
		}
	}
}

func TestMerge(t *testing.T) {
	old := &Record{SHA: "s", Tree: "t", Tasks: map[string]TaskResult{"lint": {Success: false}, "test": {Success: true}}}
	got := Merge(old, "s", "t", map[string]TaskResult{"lint": {Success: true}})
	if !got.Tasks["lint"].Success || !got.Tasks["test"].Success {
		t.Errorf("unexpected merge result: %+v", got.Tasks)
	}
	if got := Merge(old, "other", "t", nil); len(got.Tasks) != 0 {
		t.Errorf("record for a different SHA must not be merged: %+v", got.Tasks)
	}
}
