package constitution

import "testing"

func TestConstitutionHashStable(t *testing.T) {
	h1, err := Hash()
	if err != nil {
		t.Fatal(err)
	}
	h2, err := Hash()
	if err != nil {
		t.Fatal(err)
	}
	if h1 == "" || h1 != h2 {
		t.Fatalf("constitution hash unstable: %q %q", h1, h2)
	}
}
