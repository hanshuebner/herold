package identityverify

import (
	"strings"
	"testing"
	"time"
)

// TestCompose_LineLengthsWithMaximalInputs: with maximal-length addresses
// (254 octets each) and a 200-octet public base URL, no line of the
// verification email exceeds 998 octets. The fixed template text is at
// most about 330 octets per line; the variable parts are the two
// addresses and the link (base URL + path + 43-octet token).
func TestCompose_LineLengthsWithMaximalInputs(t *testing.T) {
	addr := strings.Repeat("a", 64) + "@" + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 55) + ".test"
	base := "https://" + strings.Repeat("h", 63) + "." + strings.Repeat("i", 63) + "." + strings.Repeat("j", 63) + ".example"
	out, err := Compose(ComposeInput{
		From: addr, To: addr, InitiatorEmail: addr,
		Token: strings.Repeat("T", 43), Code: "123456",
		PublicBaseURL: base, Now: time.Unix(1794121361, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(string(out.Bytes), "\r\n") {
		if len(l) > 998 {
			t.Fatalf("line of %d octets", len(l))
		}
	}
}
