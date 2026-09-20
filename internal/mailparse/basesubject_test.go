package mailparse

import "testing"

func TestNormalizeBaseSubject(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "Project update", "project update"},
		{"re prefix", "Re: Project update", "project update"},
		{"aw prefix", "Aw: Project update", "project update"},
		{"stacked", "Re: Fwd: Re: Project update", "project update"},
		{"stacked no space", "Re:Aw:Project update", "project update"},
		{"case insensitive prefix", "RE: Project Update", "project update"},
		{"whitespace folding", "  Project   update  ", "project update"},
		{"empty", "", ""},
		{"empty after strip", "Re:", ""},
		{"fw prefix", "Fw: Topic", "topic"},
		{"fwd prefix", "Fwd: Topic", "topic"},
		{"res prefix", "Res: Topic", "topic"},
		{"wg prefix", "Wg: Topic", "topic"},
		{"not a prefix", "Result: 5", "result: 5"},
		{"different topics stay different", "New topic", "new topic"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := NormalizeBaseSubject(c.in)
			if got != c.want {
				t.Fatalf("NormalizeBaseSubject(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestSubjectsThreadTogether(t *testing.T) {
	cases := []struct {
		name  string
		child string
		anc   string
		want  bool
	}{
		{"identical", "Project update", "Project update", true},
		{"reply prefix added", "Re: Project update", "Project update", true},
		{"stacked prefix added", "Re: Aw: Project update", "Project update", true},
		{"different subject", "New topic", "Project update", false},
		{"child empty", "", "Project update", true},
		{"ancestor empty", "Project update", "", true},
		{"both empty", "", "", true},
		{"case and whitespace only", "  PROJECT   UPDATE ", "project update", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := SubjectsThreadTogether(c.child, c.anc)
			if got != c.want {
				t.Fatalf("SubjectsThreadTogether(%q, %q) = %v, want %v", c.child, c.anc, got, c.want)
			}
		})
	}
}

func FuzzNormalizeBaseSubject(f *testing.F) {
	seeds := []string{
		"",
		"Re:",
		"Re: Topic",
		"Aw:Fwd:Re: Topic",
		"   ",
		"Result: 5",
		"WG: Topic",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		once := NormalizeBaseSubject(in)
		twice := NormalizeBaseSubject(once)
		if once != twice {
			t.Fatalf("NormalizeBaseSubject not idempotent: %q -> %q -> %q", in, once, twice)
		}
	})
}
