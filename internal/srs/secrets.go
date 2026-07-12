package srs

import (
	"context"
	"io"

	"github.com/hanshuebner/herold/internal/store"
)

// secretLenBytes is the size of an auto-generated SRS secret. 32 bytes
// gives HMAC-SHA1 a full-strength key; hash4's 4-character truncation
// (not the key) is what bounds the address's forgery resistance (see
// hash4's doc comment).
const secretLenBytes = 32

// Secrets loads every SRS signing/verification secret from the store, in
// ascending ID (oldest-first) order, and reports the current signing
// secret (the newest row -- highest ID -- per issue #204's rotation
// design: "newest signs, all verify").
//
// When the store holds no secret yet (first use on a fresh instance),
// Secrets generates one via randReader and persists it, so the feature
// works without any operator provisioning step -- consistent with SRS
// secrecy being a low-severity concern (a leaked secret only lets an
// attacker forge SRS bounce-routing addresses; it grants no mail-send,
// signing, or account access). A benign race is possible if two
// concurrent callers both observe an empty table and each insert a
// secret; both rows are valid and harmless (decode tries every secret;
// sign uses the row that happens to sort last), so no locking is used.
func Secrets(ctx context.Context, meta store.Metadata, randReader io.Reader) (sign []byte, all [][]byte, err error) {
	rows, err := meta.ListSRSSecrets(ctx)
	if err != nil {
		return nil, nil, err
	}
	if len(rows) == 0 {
		secret := make([]byte, secretLenBytes)
		if _, err := io.ReadFull(randReader, secret); err != nil {
			return nil, nil, err
		}
		row, err := meta.InsertSRSSecret(ctx, secret)
		if err != nil {
			return nil, nil, err
		}
		return row.Secret, [][]byte{row.Secret}, nil
	}
	all = make([][]byte, len(rows))
	for i, r := range rows {
		all[i] = r.Secret
	}
	return all[len(all)-1], all, nil
}
