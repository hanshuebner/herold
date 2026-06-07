package imapimport

import (
	"fmt"

	gosql "github.com/emersion/go-sasl"
)

// xoauth2Mechanism is the SASL mechanism name for XOAUTH2.
const xoauth2Mechanism = "XOAUTH2"

// xoauth2Client implements the XOAUTH2 SASL mechanism as used by
// Gmail and Microsoft. The client produces a single initial response
// of the form:
//
//	base64("user=" + username + "\x01auth=Bearer " + token + "\x01\x01")
//
// It implements gosql.Client.
//
// References:
//   - https://developers.google.com/gmail/imap/xoauth2-protocol
//   - https://learn.microsoft.com/en-us/exchange/client-developer/legacy-protocols/how-to-authenticate-an-imap-pop-smtp-application-by-using-oauth
type xoauth2Client struct {
	username string
	token    string
	sent     bool
}

// newXOAuth2Client returns a new XOAUTH2 SASL client. username and
// token must already be the final credentials — the token is the
// short-lived access token, NOT the refresh token.
// Neither username nor token is logged here. (REQ-IMAP-IMP-71.)
func newXOAuth2Client(username, token string) gosql.Client {
	return &xoauth2Client{username: username, token: token}
}

// Start returns the SASL mechanism name and the initial response.
// The initial response is the XOAUTH2 formatted payload.
// NEVER log the returned initialResponse — it contains the access token.
func (c *xoauth2Client) Start() (mech string, ir []byte, err error) {
	if c.username == "" {
		return "", nil, fmt.Errorf("xoauth2: username is required")
	}
	if c.token == "" {
		return "", nil, fmt.Errorf("xoauth2: access token is required")
	}
	ir = []byte("user=" + c.username + "\x01auth=Bearer " + c.token + "\x01\x01")
	c.sent = true
	return xoauth2Mechanism, ir, nil
}

// Next handles server challenges. For a successful XOAUTH2 exchange
// there are no additional challenges — the server either accepts the
// initial response or rejects it with a NO. If the server returns an
// error challenge (a JSON error payload), we return an error and
// send an empty final response so the exchange terminates cleanly.
func (c *xoauth2Client) Next(challenge []byte) ([]byte, error) {
	if !c.sent {
		return nil, fmt.Errorf("xoauth2: unexpected server challenge before initial response")
	}
	// A non-empty challenge from the server after the initial response
	// is an error notification (JSON blob). Acknowledge it with an
	// empty response so the server can send the final NO.
	if len(challenge) > 0 {
		// Do NOT log challenge — it may echo back credential-related info.
		return []byte{}, nil
	}
	return nil, nil
}
