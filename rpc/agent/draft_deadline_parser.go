package agent

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	impb "github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const draftDeadlineTimezone = "Asia/Shanghai"

type draftDeadlineEvidence struct {
	Text            string
	Source          string
	SourceMessageID int64
}

type draftDeadlineInterpretation struct {
	Text            string
	Source          string
	SourceMessageID int64
	ReferenceUnixMs int64
	Timezone        string
	Resolution      string
	DueAtUnixMs     int64
	Reason          string
}

var deadlineClockPattern = regexp.MustCompile(`^([0-9]{2}):([0-9]{2})(?::([0-9]{2})(?:\.([0-9]{1,3}))?)?$`)
var deadlineDatePatterns = []*regexp.Regexp{
	regexp.MustCompile(`^([0-9]{4})-([0-9]{2})-([0-9]{2})$`),
	regexp.MustCompile(`^([0-9]{4})/([0-9]{2})/([0-9]{2})$`),
	regexp.MustCompile(`^([0-9]{4})年([0-9]{1,2})月([0-9]{1,2})日$`),
}

// This interpreter consumes only explicitly supplied evidence and reference.
// It does not read the current clock, another message, or model-supplied UTC.
func interpretDraftDeadline(instruction string, messages []*impb.TeamGroupMessage, evidence draftDeadlineEvidence, instructionReference *int64) (draftDeadlineInterpretation, error) {
	result := draftDeadlineInterpretation{Text: evidence.Text, Source: evidence.Source, SourceMessageID: evidence.SourceMessageID, Timezone: draftDeadlineTimezone}
	invalid := func() (draftDeadlineInterpretation, error) {
		return draftDeadlineInterpretation{}, status.Error(codes.FailedPrecondition, "deadline evidence could not be verified")
	}
	if instructionReference != nil && (*instructionReference <= 0 || *instructionReference > maxDraftDueAtUnixMs) {
		return invalid()
	}
	if !utf8.ValidString(evidence.Text) || utf8.RuneCountInString(evidence.Text) > 200 || strings.TrimSpace(evidence.Text) != evidence.Text || evidence.SourceMessageID < 0 {
		return invalid()
	}
	if evidence.Text == "" {
		if evidence.Source != "none" || evidence.SourceMessageID != 0 {
			return invalid()
		}
		result.Resolution = "none"
		return result, nil
	}
	switch evidence.Source {
	case "instruction":
		if evidence.SourceMessageID != 0 || !utf8.ValidString(instruction) || !strings.Contains(instruction, evidence.Text) {
			return invalid()
		}
		if instructionReference != nil {
			result.ReferenceUnixMs = *instructionReference
		}
	case "message":
		if evidence.SourceMessageID <= 0 {
			return invalid()
		}
		var found *impb.TeamGroupMessage
		for _, message := range messages {
			if message != nil && message.GetId() == evidence.SourceMessageID {
				if found != nil {
					return invalid()
				}
				found = message
			}
		}
		if found == nil || found.GetContentType() != 1 || !utf8.ValidString(found.GetContent()) || !strings.Contains(found.GetContent(), evidence.Text) || found.GetCreatedAtUnixMs() <= 0 || found.GetCreatedAtUnixMs() > maxDraftDueAtUnixMs {
			return invalid()
		}
		result.ReferenceUnixMs = found.GetCreatedAtUnixMs()
	default:
		return invalid()
	}
	result.Resolution = "needs_input"
	result.Reason = "unsupported_expression"
	separator := strings.LastIndexByte(evidence.Text, ' ')
	if separator <= 0 {
		return result, nil
	}
	dateText, clockText := evidence.Text[:separator], evidence.Text[separator+1:]
	clock := deadlineClockPattern.FindStringSubmatch(clockText)
	if clock == nil {
		return result, nil
	}
	hour, _ := strconv.Atoi(clock[1])
	minute, _ := strconv.Atoi(clock[2])
	second, _ := strconv.Atoi(clock[3])
	if hour > 23 || minute > 59 || second > 59 {
		result.Reason = "invalid_time"
		return result, nil
	}
	var millisecond int
	if clock[4] != "" {
		millisecond, _ = strconv.Atoi(clock[4] + strings.Repeat("0", 3-len(clock[4])))
	}
	location, err := time.LoadLocation(draftDeadlineTimezone)
	if err != nil {
		return draftDeadlineInterpretation{}, status.Error(codes.Unavailable, "deadline timezone is unavailable")
	}
	var year, month, day int
	days, relative := map[string]int{"今天": 0, "明天": 1, "后天": 2}[dateText]
	if relative {
		if result.ReferenceUnixMs == 0 {
			result.Reason = "missing_reference"
			return result, nil
		}
		ref := time.UnixMilli(result.ReferenceUnixMs).In(location)
		year, localMonth, localDay := ref.Date()
		// Calendar arithmetic in UTC preserves the intended Shanghai date across
		// DST changes; assigning the requested clock happens only afterward.
		date := time.Date(year, localMonth, localDay, 0, 0, 0, 0, time.UTC).AddDate(0, 0, days)
		year = date.Year()
		month = int(date.Month())
		day = date.Day()
		return finishDraftDeadline(result, year, month, day, hour, minute, second, millisecond, location), nil
	}
	for _, pattern := range deadlineDatePatterns {
		if parts := pattern.FindStringSubmatch(dateText); parts != nil {
			year, _ = strconv.Atoi(parts[1])
			month, _ = strconv.Atoi(parts[2])
			day, _ = strconv.Atoi(parts[3])
			break
		}
	}
	if year == 0 && month == 0 && day == 0 {
		return result, nil
	}
	return finishDraftDeadline(result, year, month, day, hour, minute, second, millisecond, location), nil
}

func finishDraftDeadline(result draftDeadlineInterpretation, year, month, day, hour, minute, second, millisecond int, location *time.Location) draftDeadlineInterpretation {
	wall := time.Date(year, time.Month(month), day, hour, minute, second, millisecond*int(time.Millisecond), time.UTC)
	if year < 1 || year > 9999 || wall.Year() != year || int(wall.Month()) != month || wall.Day() != day {
		result.Reason = "invalid_date"
		return result
	}
	// Enumerate zone offsets around this wall date instead of relying on Date's
	// unspecified choice during repeated/nonexistent local times. Shanghai's
	// historical offsets are well within this two-day search window.
	start, end := wall.Add(-48*time.Hour), wall.Add(48*time.Hour)
	offsets := map[int]bool{}
	for cursor := start; !cursor.After(end); {
		local := cursor.In(location)
		_, offset := local.Zone()
		offsets[offset] = true
		_, next := local.ZoneBounds()
		if next.IsZero() || !next.After(cursor) {
			break
		}
		cursor = next
	}
	var candidates []time.Time
	for offset := range offsets {
		candidate := wall.Add(-time.Duration(offset) * time.Second)
		local := candidate.In(location)
		if local.Year() == year && int(local.Month()) == month && local.Day() == day && local.Hour() == hour && local.Minute() == minute && local.Second() == second && local.Nanosecond() == millisecond*int(time.Millisecond) {
			candidates = append(candidates, candidate)
		}
	}
	if len(candidates) == 0 {
		result.Reason = "nonexistent_local_time"
		return result
	}
	if len(candidates) > 1 {
		result.Reason = "ambiguous_local_time"
		return result
	}
	due := candidates[0].UnixMilli()
	if due <= 0 || due > maxDraftDueAtUnixMs {
		result.Reason = "out_of_range"
		return result
	}
	result.Resolution = "parsed"
	result.Reason = ""
	result.DueAtUnixMs = due
	return result
}
