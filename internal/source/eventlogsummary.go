package source

import (
	"regexp"
	"sort"
	"strings"
)

// EventLogGroup is one kind of entry in a summary of the registration log: the
// entries of the window with the same level, event, object and comment.
type EventLogGroup struct {
	Count     int    `json:"count"`
	FirstDate string `json:"firstDate"`
	LastDate  string `json:"lastDate"`
	Level     string `json:"level,omitempty"`
	Event     string `json:"event,omitempty"`
	Metadata  string `json:"metadata,omitempty"`
	Comment   string `json:"comment,omitempty" jsonschema:"comment of the latest entry of the group, cut"`
}

// summaryCommentRunes cuts the comment shown for a group: the message and the
// first frame of the stack, which is what tells one error from another.
const summaryCommentRunes = 200

// summaryKeyRunes is how much of the normalized comment decides the group.
const summaryKeyRunes = 200

var (
	summaryUUID   = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	summaryDigits = regexp.MustCompile(`[0-9]+`)
)

// summaryCommentKey is the comment with what differs between repeats of one
// error taken out: references, numbers and dates of documents, line numbers.
func summaryCommentKey(comment string) string {
	key := summaryUUID.ReplaceAllString(comment, "<uuid>")
	key = summaryDigits.ReplaceAllString(key, "#")
	key = strings.Join(strings.Fields(key), " ")
	if r := []rune(key); len(r) > summaryKeyRunes {
		key = string(r[:summaryKeyRunes])
	}
	return key
}

// SummarizeEventLog groups entries that differ only in time, user, session or
// the numbers in the comment, most frequent first. An error the base repeats a
// thousand times is one line here instead of a page of identical entries.
func SummarizeEventLog(entries []EventLogEntry) []EventLogGroup {
	type group struct {
		EventLogGroup
		key string
	}
	byKey := map[string]*group{}
	var order []*group
	for _, e := range entries {
		key := e.Level + "\x00" + e.Event + "\x00" + e.Metadata + "\x00" + summaryCommentKey(e.Comment)
		g := byKey[key]
		if g == nil {
			g = &group{key: key, EventLogGroup: EventLogGroup{
				FirstDate: e.Date, LastDate: e.Date, Level: e.Level, Event: e.Event, Metadata: e.Metadata,
			}}
			byKey[key] = g
			order = append(order, g)
		}
		g.Count++
		// Dates come as XMLСтрока (ISO 8601), so they compare as strings.
		if e.Date < g.FirstDate {
			g.FirstDate = e.Date
		}
		if e.Date >= g.LastDate {
			g.LastDate = e.Date
			g.Comment = e.Comment
		}
		if g.Comment == "" {
			g.Comment = e.Comment
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].Count != order[j].Count {
			return order[i].Count > order[j].Count
		}
		if order[i].LastDate != order[j].LastDate {
			return order[i].LastDate > order[j].LastDate
		}
		return order[i].key < order[j].key
	})
	out := make([]EventLogGroup, len(order))
	for i, g := range order {
		out[i] = g.EventLogGroup
		if r := []rune(out[i].Comment); len(r) > summaryCommentRunes {
			out[i].Comment = string(r[:summaryCommentRunes]) + "…"
		}
	}
	return out
}
