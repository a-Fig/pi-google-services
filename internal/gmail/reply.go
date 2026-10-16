package gmail

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/textproto"
	"strings"

	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi"
)

// Threading a reply takes more than a thread ID.
//
// Gmail's own API honours ThreadId server-side, so a reply sent with nothing but
// a thread ID does show up inside the conversation in the Gmail web UI. That is
// what makes this bug easy to miss. The sent message still carries no RFC 5322
// In-Reply-To or References headers, and every other mail client -- Apple Mail,
// Outlook, Thunderbird, and whatever the recipient happens to use -- threads on
// those headers alone. To them the reply is a brand new conversation.
//
// A free-form subject compounds it: a reply titled differently from the thread
// reads as unrelated even where header threading works. Google documents both
// headers and a matching subject as requirements for adding a message to a
// thread, so relying on the current leniency is unwise regardless.
//
// This file therefore derives the recipient, the subject, and both headers from
// the thread itself rather than trusting the caller to supply them.

// replyTarget is the message a reply is answering, reduced to the headers the
// reply has to carry.
type replyTarget struct {
	MessageID  string // RFC 5322 Message-ID of the message being answered
	References string // its existing References chain, if any
	Subject    string
	ReplyTo    string // Reply-To when present, otherwise From
}

// ReplyResult reports what was actually sent. Threaded is the useful field: it
// says whether Gmail really attached the reply to the requested conversation,
// which is the failure this whole file exists to prevent.
type ReplyResult struct {
	Message  *gmail.Message
	To       string
	Subject  string
	Threaded bool
	// ResolvedFromMessage is set when the caller passed a message ID rather
	// than a thread ID: it holds that message ID, so the caller can be told
	// which thread its input actually resolved to. Empty when the caller's ID
	// was already a thread ID.
	ResolvedFromMessage string
}

// ReplyToEmail answers the most recent message in a thread.
//
// to and subject may be empty, and normally should be. An empty to is answered
// to the thread's Reply-To, else its From; subject is always taken from the
// thread, because a caller cannot know the thread's subject without another
// round trip and a mismatched one breaks the conversation.
func (s *Service) ReplyToEmail(ctx context.Context, threadID, to, subject, body string, attachments []Attachment) (*ReplyResult, error) {
	target, resolvedThreadID, resolvedFromMessage, err := s.threadReplyTarget(threadID)
	if err != nil {
		return nil, fmt.Errorf("reply: %w", err)
	}

	if to == "" {
		to = target.ReplyTo
	}
	if to == "" {
		return nil, fmt.Errorf("reply: thread %s has no Reply-To or From to answer, pass an explicit recipient", resolvedThreadID)
	}
	if threadSubject := replySubject(target.Subject); threadSubject != "" {
		subject = threadSubject
	}

	msg := buildMessage(to, subject, body, attachments,
		replyHeaders(target.MessageID, referencesChain(target.References, target.MessageID)))
	msg.ThreadId = resolvedThreadID

	sent, err := s.svc.Messages.Send("me", msg).Do()
	if err != nil {
		return nil, fmt.Errorf("reply: %w", err)
	}

	return &ReplyResult{
		Message:             sent,
		To:                  to,
		Subject:             subject,
		Threaded:            sent.ThreadId == resolvedThreadID,
		ResolvedFromMessage: resolvedFromMessage,
	}, nil
}

// threadReplyTarget loads a thread and returns its most recent message as the
// one to answer, along with the thread ID that was actually resolved (which
// may differ from id) and, when id turned out to be a message ID rather than
// a thread ID, that message ID.
//
// Gmail thread IDs equal their first message's ID, so a caller that passes the
// ID of a later message in the conversation gets a 404 from Threads.Get. Since
// this is exactly the mistake an agent working only from message IDs would
// make (see pi-vi issue #102), a 404 here is retried as a message lookup
// before giving up: Messages.Get on id, then Threads.Get on that message's
// ThreadId. Any other error from the first Threads.Get call (e.g. a 400 for a
// malformed ID) is not eligible for the fallback and surfaces unchanged.
//
// Metadata format is enough for both calls and avoids pulling bodies and
// attachments back over the wire just to read a handful of headers.
func (s *Service) threadReplyTarget(id string) (target *replyTarget, threadID string, resolvedFromMessage string, err error) {
	thread, err := s.svc.Threads.Get("me", id).
		Format("metadata").
		MetadataHeaders("Message-ID", "References", "Subject", "From", "Reply-To").
		Do()
	if err != nil {
		if !isNotFound(err) {
			return nil, "", "", fmt.Errorf("load thread %s: %w", id, err)
		}

		msg, msgErr := s.svc.Messages.Get("me", id).Format("minimal").Do()
		if msgErr != nil {
			if isNotFound(msgErr) {
				return nil, "", "", fmt.Errorf("neither a thread nor a message with id %s exists", id)
			}
			return nil, "", "", fmt.Errorf("load message %s: %w", id, msgErr)
		}

		thread, err = s.svc.Threads.Get("me", msg.ThreadId).
			Format("metadata").
			MetadataHeaders("Message-ID", "References", "Subject", "From", "Reply-To").
			Do()
		if err != nil {
			return nil, "", "", fmt.Errorf("load thread %s: %w", msg.ThreadId, err)
		}
		resolvedFromMessage = id
	}
	if len(thread.Messages) == 0 {
		return nil, "", "", fmt.Errorf("thread %s has no messages", thread.Id)
	}
	return extractReplyTarget(thread.Messages[len(thread.Messages)-1]), thread.Id, resolvedFromMessage, nil
}

// isNotFound reports whether err is a googleapi 404, including when wrapped.
// A nil err, a non-googleapi error, or any other status (a 400 for a
// malformed ID, say) all report false so that only "no such thread" triggers
// the message-ID fallback in threadReplyTarget.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var gErr *googleapi.Error
	if errors.As(err, &gErr) {
		return gErr.Code == http.StatusNotFound
	}
	return false
}

// extractReplyTarget pulls the threading headers out of a message. Header names
// are canonicalized because Gmail returns Message-ID and Message-Id from
// different senders.
func extractReplyTarget(msg *gmail.Message) *replyTarget {
	target := &replyTarget{}
	if msg == nil || msg.Payload == nil {
		return target
	}
	var from string
	for _, h := range msg.Payload.Headers {
		switch textproto.CanonicalMIMEHeaderKey(h.Name) {
		case "Message-Id":
			target.MessageID = strings.TrimSpace(h.Value)
		case "References":
			target.References = strings.TrimSpace(h.Value)
		case "Subject":
			target.Subject = strings.TrimSpace(h.Value)
		case "Reply-To":
			target.ReplyTo = strings.TrimSpace(h.Value)
		case "From":
			from = strings.TrimSpace(h.Value)
		}
	}
	if target.ReplyTo == "" {
		target.ReplyTo = from
	}
	return target
}

// replySubject returns the subject a reply must carry, which is the thread's own
// subject. The Re: prefix is added once and never stacked.
func replySubject(threadSubject string) string {
	subject := strings.TrimSpace(threadSubject)
	if subject == "" || hasReplyPrefix(subject) {
		return subject
	}
	return "Re: " + subject
}

// hasReplyPrefix reports whether a subject already opens with a reply marker.
func hasReplyPrefix(subject string) bool {
	lower := strings.ToLower(subject)
	return strings.HasPrefix(lower, "re:") || strings.HasPrefix(lower, "re :")
}

// referencesChain appends the answered message to the existing References chain.
// Mail clients walk this chain to draw a conversation, so it accumulates rather
// than being replaced, and never repeats an ID already in it.
func referencesChain(existing, messageID string) string {
	switch {
	case messageID == "":
		return existing
	case existing == "":
		return messageID
	case strings.Contains(existing, messageID):
		return existing
	default:
		return existing + " " + messageID
	}
}

// replyHeaders renders the two threading headers. Gmail wants both; other mail
// clients thread on either, so an empty Message-ID degrades to no headers rather
// than to a malformed one.
func replyHeaders(inReplyTo, references string) string {
	var b strings.Builder
	if inReplyTo != "" {
		fmt.Fprintf(&b, "In-Reply-To: %s\r\n", inReplyTo)
	}
	if references != "" {
		fmt.Fprintf(&b, "References: %s\r\n", references)
	}
	return b.String()
}
