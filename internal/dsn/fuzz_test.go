package dsn_test

// DSN wire-parser fuzz target (STANDARDS.md §8.2). Parse composes
// mailparse.Parse (already fuzzed independently in internal/mailparse)
// with this package's own message/delivery-status field-block reader,
// so the target here focuses on the additional surface: locating the
// delivery-status part, splitting it into blank-line-delimited field
// groups, and classifying them.
//
// Every seed must produce a result without panicking; Classification
// must always be one of the three defined constants (guaranteed by the
// type, checked here defensively in case of a future refactor); and a
// non-nil error must never come paired with a non-Unknown
// classification (an error means "could not even parse the message",
// which must never smuggle out a stray Hard/Soft guess).
import (
	"testing"

	"github.com/hanshuebner/herold/internal/dsn"
)

func FuzzParse(f *testing.F) {
	seeds := []string{
		postfixHardBounce,
		eximHardBounce,
		gmailHardBounce,
		outlookSoftBounce,
		nonConformantOutlookBounce,
		successDeliveryReport,
		"",
		"not an email at all",
		"From: a@b.test\r\n\r\n",
		"From: a@b.test\r\nContent-Type: multipart/report; boundary=X\r\n\r\n--X\r\n\r\n--X--\r\n",
		"From: a@b.test\r\nContent-Type: multipart/report; boundary=X\r\n\r\n--X\r\nContent-Type: message/delivery-status\r\n\r\nAction: failed\r\nStatus: 5\r\n\r\n--X--\r\n",
		"From: a@b.test\r\nContent-Type: message/delivery-status\r\n\r\nAction: failed\r\nStatus: 5.1.1\r\n",
		"From: a@b.test\r\nContent-Type: multipart/report; boundary=X\r\n\r\n--X\r\nContent-Type: message/delivery-status\r\n\r\n\r\n\r\n\r\n--X--\r\n",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip("oversize input; production caps well below this via mailparse.ParseOptions.MaxSize")
		}
		report, err := dsn.Parse(data)
		if err != nil {
			if report.Classification != dsn.ClassificationUnknown {
				t.Fatalf("error case returned non-zero Classification %v (report=%+v, err=%v)", report.Classification, report, err)
			}
			return
		}
		switch report.Classification {
		case dsn.ClassificationUnknown, dsn.ClassificationSoft, dsn.ClassificationHard:
		default:
			t.Fatalf("unexpected Classification value %v", report.Classification)
		}
	})
}
