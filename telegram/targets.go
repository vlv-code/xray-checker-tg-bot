package telegram

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/mymmrac/telego"
)

// ChatTarget identifies a destination for outgoing bot messages: either a
// whole chat (a private chat, a plain group, or a forum supergroup without a
// specific topic) or one forum topic inside a supergroup.
type ChatTarget struct {
	ChatID   int64
	ThreadID int // 0 = whole chat / General topic
}

// ParseChatTargets converts TELEGRAM_CHAT_IDS entries into send targets.
// Accepted forms: "<chat_id>" (whole chat) and "<chat_id>:<topic_id>" (one
// forum topic), e.g. "123456789", "-100987654321", "-100987654321:42".
func ParseChatTargets(entries []string) ([]ChatTarget, error) {
	targets := make([]ChatTarget, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, raw := range entries {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}

		chatPart := entry
		threadPart := ""
		if idx := strings.LastIndex(entry, ":"); idx != -1 {
			chatPart = entry[:idx]
			threadPart = entry[idx+1:]
			if threadPart == "" {
				return nil, fmt.Errorf("invalid chat target %q: empty topic id after ':'", raw)
			}
		}

		chatID, err := strconv.ParseInt(chatPart, 10, 64)
		if err != nil || chatID == 0 {
			return nil, fmt.Errorf("invalid chat target %q: expected <chat_id> or <chat_id>:<topic_id>", raw)
		}

		threadID := 0
		if threadPart != "" {
			t, err := strconv.Atoi(threadPart)
			if err != nil || t <= 0 {
				return nil, fmt.Errorf("invalid chat target %q: topic id must be a positive number", raw)
			}
			threadID = t
		}

		ct := ChatTarget{ChatID: chatID, ThreadID: threadID}
		key := ct.targetKey()
		if seen[key] {
			continue // exact duplicate entry: keep one copy
		}
		seen[key] = true
		targets = append(targets, ct)
	}
	return targets, nil
}

// targetKey returns a stable string key identifying the target, either
// "<chat_id>" or "<chat_id>:<thread_id>". It is used for per-target caches
// such as the interactive menu message.
func (t ChatTarget) targetKey() string {
	if t.ThreadID > 0 {
		return fmt.Sprintf("%d:%d", t.ChatID, t.ThreadID)
	}
	return strconv.FormatInt(t.ChatID, 10)
}

// targetFromMessage derives the reply destination of an incoming message so
// replies land in the same forum topic the command was issued in.
func targetFromMessage(msg *telego.Message) ChatTarget {
	if msg == nil {
		return ChatTarget{}
	}
	return ChatTarget{ChatID: msg.Chat.ID, ThreadID: msg.MessageThreadID}
}
