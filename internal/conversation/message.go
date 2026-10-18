package conversation

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

// ErrInvalidBody wraps a body that breaks ValidateMessageBody or, as
// ErrUnknownReference, references a member posting cannot find.
var ErrInvalidBody = errors.New("invalid message body")

// MaxMessageBodyLength bounds typed and stored bodies in Unicode code points.
const MaxMessageBodyLength = 4000

// ErrMessageNotFound means no message has the requested key in the caller's organisation
// and channel, including when the message exists in another scope.
var ErrMessageNotFound = errors.New("message not found")

// ErrQuoteNotFound means the quoted message is not in the poster's
// organisation and channel: unknown, another organisation's and another
// channel's are the same error, so the answer reveals none of them.
var ErrQuoteNotFound = errors.New("quoted message not found")

// Format names the grammar a stored body is read with (decision 39). A
// stored message keeps its format (decision 18).
type Format string

// The formats the message table's CHECK allows.
const (
	FormatPlainText Format = "plain_text"
	FormatChatV1    Format = "chat_v1"
)

// Message is a post identified by ID, its body read under Format. EventSeq
// orders it within its organisation.
type Message struct {
	ID, OrganizationID, ChannelID, TopicID, MemberID kernel.ID
	Body                                             string
	Format                                           Format
	EventSeq                                         int64
	CreatedAt                                        time.Time
	// TypedBodyProof verifies the whole normalized typed body on keyed retries.
	TypedBodyProof TypedBodyProof
	// Pinned is the message's current pin state, the same for every member.
	Pinned bool
	// Quote is the earlier message this one quotes, or nil.
	Quote *Quote
}

// Quote is a message's copy of an earlier message's text in the same
// channel (decision 38). It belongs to the quoting message: the text stays
// as posted whatever happens to the original.
type Quote struct {
	// MessageID is the original, or nil once it is deleted.
	MessageID *kernel.ID
	MemberID  kernel.ID
	Text      string
}

// ValidateMessageBody normalizes line endings and trims the whole body source text.
// It preserves other text, including decomposed characters and emoji joiners.
func ValidateMessageBody(value string) (string, error) {
	if !utf8.ValidString(value) {
		return "", fmt.Errorf("message body must contain valid UTF-8")
	}
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
	if strings.ContainsFunc(value, func(r rune) bool {
		return unicode.IsControl(r) && r != '\n' && r != '\t' || r == '\u2028' || r == '\u2029' || isBidiEmbeddingOrOverride(r)
	}) {
		return "", fmt.Errorf("message body contains forbidden control characters or separators")
	}
	value = strings.TrimSpace(value)
	if length := utf8.RuneCountInString(value); length < 1 || length > MaxMessageBodyLength {
		return "", fmt.Errorf("message body must contain 1–4000 Unicode code points")
	}
	return value, nil
}

// ValidateQuotePart normalizes a part under the body rules and requires it
// to occur in the original's extracted text, supplied after a scoped read.
func ValidateQuotePart(value, original string) (string, error) {
	part, err := ValidateMessageBody(value)
	if err != nil {
		return "", fmt.Errorf("%w: quote part: %w", ErrInvalidBody, err)
	}
	if !strings.Contains(original, part) {
		return "", fmt.Errorf("%w: quote part is absent from original", ErrInvalidBody)
	}
	return part, nil
}

// isBidiEmbeddingOrOverride reports LRE, RLE, PDF, LRO and RLO (U+202A–U+202E).
// They can make stored text read differently from how it is stored (for
// example a spoofed URL), and Unicode discourages them in new text. The
// isolates LRI, RLI, FSI and PDI (U+2066–U+2069), and marks such as LRM,
// RLM and ALM, are allowed on purpose: they are the recommended way to mix
// directions in plain text. Views must show each body with dir="auto" or in
// a <bdi> so its direction stays inside it; the message list does so from #78.
func isBidiEmbeddingOrOverride(r rune) bool { return r >= '\u202a' && r <= '\u202e' }
