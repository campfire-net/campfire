// Package github implements the GitHub Issues transport for the Campfire protocol.
// Each campfire maps to one GitHub Issue; messages are Issue comments.
package github

import (
	"encoding/base64"
	"errors"
	"strings"

	cfencoding "github.com/3dl-dev/campfire/pkg/encoding"
	"github.com/3dl-dev/campfire/pkg/message"
)

const commentPrefix = "campfire-msg-v1:"

// ErrNotCampfireMessage is returned by DecodeComment when the comment body
// does not carry the campfire-msg-v1: prefix (i.e. it is a human comment).
var ErrNotCampfireMessage = errors.New("not a campfire message")

// EncodeComment serializes a Message to the campfire-msg-v1 comment format:
//
//	campfire-msg-v1:<base64(cbor(msg))>
func EncodeComment(msg *message.Message) (string, error) {
	raw, err := cfencoding.Marshal(msg)
	if err != nil {
		return "", err
	}
	return commentPrefix + base64.StdEncoding.EncodeToString(raw), nil
}

// DecodeComment parses a GitHub Issue comment body and returns the embedded
// Message. Returns ErrNotCampfireMessage if the body lacks the required prefix.
func DecodeComment(body string) (*message.Message, error) {
	if !strings.HasPrefix(body, commentPrefix) {
		return nil, ErrNotCampfireMessage
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(body, commentPrefix))
	if err != nil {
		return nil, err
	}
	var msg message.Message
	if err := cfencoding.Unmarshal(raw, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}
